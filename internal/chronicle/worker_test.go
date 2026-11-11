package chronicle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/power"
)

type workerRepo struct {
	mu   sync.Mutex
	rows map[string]Summary
	err  error
}

func (r *workerRepo) PutChronicle(_ context.Context, s Summary) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.rows[s.HourID] = s
	return nil
}
func (r *workerRepo) GetChronicle(_ context.Context, id string) (Summary, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.rows[id]
	return s, ok, nil
}

type workerPolicy struct {
	mu    sync.Mutex
	value power.Effective
	err   error
	calls int
}

func (p *workerPolicy) Effective(context.Context) (power.Effective, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.value, p.err
}
func (p *workerPolicy) callCount() int     { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }
func (p *workerPolicy) setError(err error) { p.mu.Lock(); defer p.mu.Unlock(); p.err = err }

type workerModel struct {
	mu       sync.Mutex
	count    int
	calls    int
	entered  chan struct{}
	release  chan struct{}
	err      error
	followUp bool
}

func (m *workerModel) CountInputTokens(context.Context, Packed) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count, nil
}
func (m *workerModel) Generate(ctx context.Context, p Packed, _ int) (Summary, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	if m.entered != nil {
		select {
		case m.entered <- struct{}{}:
		default:
		}
	}
	if m.release != nil {
		select {
		case <-m.release:
		case <-ctx.Done():
			return Summary{}, ctx.Err()
		}
	}
	if m.err != nil {
		return Summary{}, m.err
	}
	summary := Summary{HourID: p.HourID, Start: p.Start, End: p.End, Text: "A concise recap.", SourceIDs: append([]string(nil), p.SourceIDs...)}
	if m.followUp {
		summary.FollowUp = &FollowUp{SourceID: p.SourceIDs[0], Text: "Who owns the meeting follow-up?"}
	}
	return summary, nil
}
func (m *workerModel) callCount() int { m.mu.Lock(); defer m.mu.Unlock(); return m.calls }
func workerHour(t *testing.T) Hour {
	t.Helper()
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	o, err := capture.SanitizeObservation("editor.app", "", "reviewed project notes", 2048)
	if err != nil {
		t.Fatal(err)
	}
	return Hour{Start: start, End: start.Add(time.Hour), Evidence: []Evidence{{Observation: o, At: start.Add(time.Minute), Salience: 10}}}
}
func balanced() power.Effective {
	e, _ := power.Resolve(power.Config{Mode: power.Balanced}, power.SystemState{})
	return e
}
func performance() power.Effective {
	e, _ := power.Resolve(power.Config{Mode: power.Performance}, power.SystemState{})
	return e
}

func TestWorkerRequiresDefaultOffOptInAndLocalRoute(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  bool
		provider power.Provider
	}{{"default off", false, power.LocalProvider}, {"remote", true, power.RemoteProvider}} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &workerRepo{rows: map[string]Summary{}}
			policy := &workerPolicy{value: balanced()}
			model := &workerModel{count: 10}
			w := NewWorker(repo, policy, model, WorkerConfig{Enabled: tc.enabled, Provider: tc.provider})
			defer w.Close()
			if err := w.Schedule(context.Background(), workerHour(t)); !errors.Is(err, ErrWorkerDisabled) {
				t.Fatalf("Schedule error=%v", err)
			}
			if model.callCount() != 0 || policy.callCount() != 0 {
				t.Fatal("disabled or remote worker touched policy/model")
			}
		})
	}
}

func TestWorkerPersistsOnlyCitedMeetingFollowUpQuestion(t *testing.T) {
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	observation, err := capture.SanitizeObservation("editor.app", "", "Meeting: discussed a missing launch owner.", 2048)
	if err != nil {
		t.Fatal(err)
	}
	hour := Hour{Start: start, End: start.Add(time.Hour), Evidence: []Evidence{{Observation: observation, At: start.Add(time.Minute), Salience: 10}}}
	repo := &workerRepo{rows: map[string]Summary{}}
	worker := NewWorker(repo, &workerPolicy{value: balanced()}, &workerModel{count: 10, followUp: true}, WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer worker.Close()
	if err := worker.Process(context.Background(), hour); err != nil {
		t.Fatal(err)
	}
	row, found, err := repo.GetChronicle(context.Background(), "hour:20261106T10Z")
	if err != nil || !found || row.FollowUp == nil || row.FollowUp.SourceID != observation.ContentID() {
		t.Fatalf("worker persisted recap follow-up = %+v, found=%t err=%v", row.FollowUp, found, err)
	}
	generic, err := capture.SanitizeObservation("editor.app", "", "Reviewed project notes.", 2048)
	if err != nil {
		t.Fatal(err)
	}
	hour.Evidence[0] = Evidence{Observation: generic, At: start.Add(time.Minute), Salience: 10}
	if err := worker.Process(context.Background(), hour); err == nil {
		t.Fatal("worker accepted a follow-up without meeting evidence")
	}
}

func TestWorkerAdmitsOnlyEffectiveLocalBackgroundBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		effective power.Effective
		tokens    int
		want      bool
	}{{"balanced unknown system", balanced(), 10, true}, {"saver", power.Effective{Mode: power.Saver}, 10, false}, {"over token cap", balanced(), 2048, false}, {"performance", performance(), 10, true}} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &workerRepo{rows: map[string]Summary{}}
			policy := &workerPolicy{value: tc.effective}
			model := &workerModel{count: tc.tokens}
			w := NewWorker(repo, policy, model, WorkerConfig{Enabled: true, Provider: power.LocalProvider})
			defer w.Close()
			err := w.Process(context.Background(), workerHour(t))
			if tc.want && err != nil {
				t.Fatalf("Process()=%v", err)
			}
			if !tc.want && err == nil {
				t.Fatal("Process unexpectedly admitted")
			}
			if (model.callCount() == 1) != tc.want {
				t.Fatalf("model calls=%d, want admitted=%t", model.callCount(), tc.want)
			}
		})
	}
}

func TestWorkerCoalescesDuplicatePendingHoursAndCloseCancelsModel(t *testing.T) {
	repo := &workerRepo{rows: map[string]Summary{}}
	policy := &workerPolicy{value: performance()}
	model := &workerModel{count: 1, entered: make(chan struct{}, 1), release: make(chan struct{})}
	w := NewWorker(repo, policy, model, WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	hour := workerHour(t)
	if err := w.Schedule(context.Background(), hour); err != nil {
		t.Fatal(err)
	}
	select {
	case <-model.entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	if err := w.Schedule(context.Background(), hour); !errors.Is(err, ErrWorkerDuplicate) {
		t.Fatalf("duplicate schedule=%v", err)
	}
	closed := make(chan struct{})
	go func() { w.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel active generation")
	}
}

func TestWorkerFreshPolicyAndPersistenceAreRequired(t *testing.T) {
	repo := &workerRepo{rows: map[string]Summary{}}
	policy := &workerPolicy{value: performance(), err: errors.New("policy unavailable")}
	model := &workerModel{count: 1}
	w := NewWorker(repo, policy, model, WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer w.Close()
	if err := w.Process(context.Background(), workerHour(t)); err == nil {
		t.Fatal("policy failure was ignored")
	}
	if model.callCount() != 0 {
		t.Fatal("model called without fresh policy")
	}
	policy.setError(nil)
	repo.err = errors.New("deleted source")
	if err := w.Process(context.Background(), workerHour(t)); err == nil {
		t.Fatal("persistence/source-deletion failure hidden")
	}
}

func TestWorkerConcurrencyNeverExceedsFreshPowerAdmission(t *testing.T) {
	repo := &workerRepo{rows: map[string]Summary{}}
	policy := &workerPolicy{value: performance()}
	model := &workerModel{count: 1, entered: make(chan struct{}, 3), release: make(chan struct{}, 3)}
	w := NewWorker(repo, policy, model, WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer w.Close()
	first := workerHour(t)
	second := workerHour(t)
	second.Start = second.Start.Add(time.Hour)
	second.End = second.Start.Add(time.Hour)
	second.Evidence[0].At = second.Start.Add(time.Minute)
	if err := w.Schedule(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := w.Schedule(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-model.entered:
		case <-time.After(time.Second):
			t.Fatalf("Performance policy did not admit two jobs; started %d", i)
		}
	}
	model.release <- struct{}{}
	model.release <- struct{}{}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		repo.mu.Lock()
		count := len(repo.rows)
		repo.mu.Unlock()
		if count == 2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("both admitted chronicle jobs did not persist")
}

type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func TestWorkerLogJobErrorClassification(t *testing.T) {
	handler := &captureHandler{}
	w := &Worker{logger: slog.New(handler)}
	w.logJobError(model.ErrOllamaUnavailable)
	w.logJobError(fmt.Errorf("wrapped: %w", model.ErrOllamaUnavailable))
	w.logJobError(model.ErrCompatibleUnavailable)
	if len(handler.records) != 1 {
		t.Fatalf("unavailable provider logged %d lines, want exactly 1", len(handler.records))
	}
	if r := handler.records[0]; r.Level != slog.LevelInfo || r.Message != "chronicle job deferred" {
		t.Fatalf("got %v %q", r.Level, r.Message)
	}
	w.logJobError(ErrWorkerPolicy)
	w.logJobError(model.ErrOllamaResponse)
	if len(handler.records) != 3 {
		t.Fatalf("real failures must each log, got %d records", len(handler.records))
	}
	for _, r := range handler.records[1:] {
		if r.Level != slog.LevelWarn || r.Message != "chronicle job failed" {
			t.Fatalf("got %v %q", r.Level, r.Message)
		}
	}
}
