package chronicle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/model"
)

type prioritySummaryModel struct {
	mu      sync.Mutex
	calls   int
	entered chan int
	release chan struct{}
	failure error
}

func (*prioritySummaryModel) CountInputTokens(context.Context, Packed) (int, error) { return 10, nil }
func (m *prioritySummaryModel) Generate(ctx context.Context, p Packed, _ int) (Summary, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.mu.Unlock()
	m.entered <- n
	if n == 1 {
		<-m.release
		return Summary{}, m.failure
	}
	return Summary{HourID: p.HourID, Start: p.Start, End: p.End, Text: "A concise recap.", SourceIDs: append([]string(nil), p.SourceIDs...)}, ctx.Err()
}
func (m *prioritySummaryModel) count() int { m.mu.Lock(); defer m.mu.Unlock(); return m.calls }

type prioritySummaryRepo struct {
	workerRepo
	present bool
	source  capture.SanitizedObservation
	at      time.Time
}

func (r *prioritySummaryRepo) GetObservationWithCaptureTime(context.Context, string) (capture.SanitizedObservation, time.Time, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.source, r.at, r.present, nil
}

func TestPreemptedRecapRetainsQueuedHourAndRevalidatesBeforeRetry(t *testing.T) {
	for _, operation := range []string{"retry", "revisited", "deletion", "replacement", "policy", "close", "actual-error"} {
		t.Run(operation, func(t *testing.T) {
			hour := workerHour(t)
			packed, _ := PackHour(hour)
			repo := &prioritySummaryRepo{workerRepo: workerRepo{rows: map[string]Summary{}}, present: true, source: hour.Evidence[0].Observation, at: hour.Evidence[0].At.Add(-30 * time.Second)}
			policy := &workerPolicy{value: balanced()}
			generator := &prioritySummaryModel{entered: make(chan int, 4), release: make(chan struct{}), failure: model.ErrBackgroundPreempted}
			if operation == "actual-error" {
				generator.failure = errors.New("synthetic actual model failure")
			}
			worker := NewWorker(repo, policy, generator, WorkerConfig{Enabled: true, Provider: "local"})
			defer worker.Close()
			if err := worker.Schedule(context.Background(), hour); err != nil {
				t.Fatal(err)
			}
			select {
			case <-generator.entered:
			case <-time.After(time.Second):
				t.Fatal("initial recap did not start")
			}
			if operation == "deletion" {
				repo.mu.Lock()
				repo.present = false
				repo.mu.Unlock()
			}
			if operation == "replacement" {
				repo.mu.Lock()
				repo.at = repo.at.Add(time.Minute)
				repo.mu.Unlock()
			}
			if operation == "policy" {
				policy.setError(ErrWorkerPolicy)
			}
			closed := make(chan struct{})
			if operation == "close" {
				go func() { worker.Close(); close(closed) }()
				<-worker.ctx.Done()
			}
			close(generator.release)
			if operation == "retry" || operation == "revisited" {
				time.Sleep(20 * time.Millisecond)
				if status := worker.Status(); status.State != "deferred" || status.Reason != "foreground" || status.LastErrorCode != "" {
					t.Fatal("actual preemption missing from status", status)
				}
				if err := worker.Schedule(context.Background(), hour); !errors.Is(err, ErrWorkerDuplicate) {
					t.Fatal("preempted hour lost bounded dedup membership", err)
				}
				select {
				case n := <-generator.entered:
					if n != 2 {
						t.Fatal("recap duplicated")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("preempted recap was dropped")
				}
				deadline := time.Now().Add(time.Second)
				for {
					_, found, _ := repo.GetChronicle(context.Background(), packed.HourID)
					if found {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("retried recap not saved")
					}
					time.Sleep(time.Millisecond)
				}
			} else {
				if operation == "close" {
					select {
					case <-closed:
					case <-time.After(time.Second):
						t.Fatal("Close failed to join deferred recap")
					}
				} else {
					time.Sleep(400 * time.Millisecond)
				}
				if generator.count() != 1 {
					t.Fatal("recap retried after deletion/policy/close or actual error")
				}
			}
		})
	}
}

func TestPreemptedRecapCannotRetryWithoutImmutableSourceReader(t *testing.T) {
	repo := &workerRepo{rows: map[string]Summary{}}
	generator := &prioritySummaryModel{entered: make(chan int, 4), release: make(chan struct{}), failure: model.ErrBackgroundPreempted}
	worker := NewWorker(repo, &workerPolicy{value: balanced()}, generator, WorkerConfig{Enabled: true, Provider: "local"})
	defer worker.Close()
	if err := worker.Schedule(context.Background(), workerHour(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-generator.entered:
	case <-time.After(time.Second):
		t.Fatal("initial model did not start")
	}
	close(generator.release)
	time.Sleep(400 * time.Millisecond)
	if generator.count() != 1 {
		t.Fatal("preempted recap retried without source membership authority")
	}
}

func TestWorkerStatusTracksActualCompletionPolicyAndNoStatusSideEffects(t *testing.T) {
	repo := &workerRepo{rows: map[string]Summary{}}
	policy := &workerPolicy{value: balanced()}
	generator := &workerModel{count: 10}
	worker := NewWorker(repo, policy, generator, WorkerConfig{Enabled: true, Provider: "local"})
	defer worker.Close()
	if err := worker.Process(context.Background(), workerHour(t)); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		status := worker.Status()
		if status.Completed != 1 || status.LastCompletedAt.IsZero() || status.State != "idle" || status.Queued != 0 || status.Running != 0 {
			t.Fatal("real completion missing from status", status)
		}
	}
	if generator.callCount() != 1 || policy.callCount() != 1 {
		t.Fatal("status triggered model or power probe")
	}
	policy.setError(ErrWorkerPolicy)
	if err := worker.Process(context.Background(), workerHour(t)); err == nil {
		t.Fatal("policy fixture not applied")
	}
	status := worker.Status()
	if status.State != "unavailable" || status.Reason != "policy" || status.LastErrorCode != "policy" || status.LastErrorAt.IsZero() || status.Completed != 1 {
		t.Fatal("policy status lost last successful completion", status)
	}
}
