package chronicle

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/power"
)

type rollupRepo struct {
	mu      sync.Mutex
	hours   map[string]Summary
	rollups map[string]Rollup
	puts    int
}

func newRollupRepo() *rollupRepo {
	return &rollupRepo{hours: map[string]Summary{}, rollups: map[string]Rollup{}}
}

func (r *rollupRepo) PutChronicle(_ context.Context, s Summary) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hours[s.HourID] = s
	return nil
}
func (r *rollupRepo) GetChronicle(_ context.Context, id string) (Summary, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.hours[id]
	return s, ok, nil
}
func (r *rollupRepo) ListChronicles(_ context.Context, start, end time.Time, _ string, _ int) ([]Summary, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Summary
	for _, s := range r.hours {
		if !s.Start.Before(start) && s.Start.Before(end) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HourID < out[j].HourID })
	return out, "", nil
}
func (r *rollupRepo) PutChronicleRollup(_ context.Context, v Rollup) error {
	if err := ValidateRollup(v); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.puts++
	r.rollups[v.ID] = v
	return nil
}
func (r *rollupRepo) MarkChronicleRollupDirty(_ context.Context, id, zone string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.rollups[id]
	if !ok {
		var err error
		if v, err = NewRollupPlaceholder(id, zone); err != nil {
			return err
		}
	}
	v.Dirty = true
	r.rollups[id] = v
	return nil
}
func (r *rollupRepo) GetChronicleRollup(_ context.Context, id string) (Rollup, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.rollups[id]
	return v, ok, nil
}
func (r *rollupRepo) ListChronicleRollups(_ context.Context, level string, start, end time.Time, _ string, _ int) ([]Rollup, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Rollup
	for _, v := range r.rollups {
		if v.Level == level && !v.Start.Before(start) && v.Start.Before(end) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, "", nil
}
func (r *rollupRepo) markDirty(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.rollups[id]
	v.Dirty = true
	r.rollups[id] = v
}
func (r *rollupRepo) rollup(id string) Rollup {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rollups[id]
}

type rollupModel struct {
	*workerModel
	mu      sync.Mutex
	prompts []RollupPrompt
	output  string
}

func (m *rollupModel) CountRollupTokens(context.Context, RollupPrompt) (int, error) { return 200, nil }
func (m *rollupModel) GenerateRollup(_ context.Context, p RollupPrompt, _ int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prompts = append(m.prompts, p)
	if m.output != "" {
		return m.output, nil
	}
	return "- Worked through the planned tasks.", nil
}
func (m *rollupModel) RollupModel(context.Context) (string, error) { return "local-test-model", nil }
func (m *rollupModel) rollupCalls() int                            { m.mu.Lock(); defer m.mu.Unlock(); return len(m.prompts) }

type rollupFixture struct {
	repo   *rollupRepo
	model  *rollupModel
	worker *Worker
	now    time.Time
}

func newRollupFixture(t *testing.T, provider power.Provider, now time.Time) *rollupFixture {
	t.Helper()
	f := &rollupFixture{repo: newRollupRepo(), model: &rollupModel{workerModel: &workerModel{count: 10}}, now: now}
	f.worker = NewWorker(f.repo, &workerPolicy{value: balanced()}, f.model, WorkerConfig{Enabled: true, Provider: provider, Now: func() time.Time { return f.now }, Zone: func() string { return "UTC" }})
	t.Cleanup(f.worker.Close)
	return f
}

func (f *rollupFixture) addHour(t *testing.T, day, hour int, text string, sources ...string) {
	t.Helper()
	start := time.Date(2026, 11, day, hour, 0, 0, 0, time.UTC)
	if len(sources) == 0 {
		sources = []string{rollupSourceID(day*100 + hour)}
	}
	sort.Strings(sources)
	s := Summary{HourID: "hour:" + start.Format("20060102T15Z"), Start: start, End: start.Add(time.Hour), Text: text, SourceIDs: sources}
	if err := f.repo.PutChronicle(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	id := "day:" + start.Format("2006-01-02")
	f.repo.mu.Lock()
	existing, ok := f.repo.rollups[id]
	f.repo.mu.Unlock()
	if ok {
		f.repo.markDirty(id)
		return
	}
	placeholder, err := NewRollupPlaceholder(id, "UTC")
	_ = existing
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.PutChronicleRollup(context.Background(), placeholder); err != nil {
		t.Fatal(err)
	}
}

func TestRollupWorkerGeneratesDayAfterItsHoursThenWeek(t *testing.T) {
	f := newRollupFixture(t, power.LocalProvider, time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC))
	f.addHour(t, 15, 9, "Reviewed the migration plan.")
	f.addHour(t, 15, 14, "Wrote the rollout notes.")
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	day := f.repo.rollup("day:2026-11-15")
	if day.Text == "" || day.Dirty || day.Model != "local-test-model" || len(day.ChildIDs) != 2 || day.ChildIDs[0] != "hour:20261115T09Z" || len(day.SourceIDs) != 2 || len(day.InputHash) != 64 || day.GeneratedAt.IsZero() {
		t.Fatalf("day rollup = %+v", day)
	}
	prompt := f.model.prompts[0].User
	if !strings.Contains(prompt, "09:00-10:00: Reviewed the migration plan.") || !strings.Contains(prompt, "14:00-15:00: Wrote the rollout notes.") || strings.Index(prompt, "09:00") > strings.Index(prompt, "14:00") {
		t.Fatalf("day prompt does not carry labelled ordered hours: %q", prompt)
	}
	// 15 November 2026 is a Sunday: its ISO week has ended on Monday morning.
	week := f.repo.rollup("week:2026-W46")
	if week.Text == "" || week.Dirty || len(week.ChildIDs) != 1 || week.ChildIDs[0] != "day:2026-11-15" || len(week.SourceIDs) != 2 {
		t.Fatalf("week rollup was not generated after its day: %+v", week)
	}
	if f.model.rollupCalls() != 2 {
		t.Fatalf("model calls = %d, want day then week", f.model.rollupCalls())
	}
	status := f.worker.Status()
	if status.RollupsDirty != 0 || status.RollupsPending != 0 || status.RollupReason != "" || status.Completed != 2 {
		t.Fatalf("status = %+v", status)
	}
}

func TestRollupWorkerWaitsForLocalDayToEndUnlessAlreadyGenerated(t *testing.T) {
	f := newRollupFixture(t, power.LocalProvider, time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC))
	f.addHour(t, 15, 9, "Reviewed the plan.")
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.model.rollupCalls() != 0 || f.repo.rollup("day:2026-11-15").Text != "" {
		t.Fatal("an unfinished day was summarized")
	}
	if status := f.worker.Status(); status.RollupsDirty != 1 || status.RollupsPending != 0 {
		t.Fatalf("an unfinished placeholder should be dirty but not pending: %+v", status)
	}
	f.now = time.Date(2026, 11, 16, 0, 5, 0, 0, time.UTC)
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.model.rollupCalls() == 0 || f.repo.rollup("day:2026-11-15").Text == "" {
		t.Fatal("the ended day was not summarized")
	}
	// A generated day whose input changes is regenerated even before it ends.
	f.now = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	f.addHour(t, 22, 9, "Early work.")
	f.model.output = "- First pass."
	f.now = time.Date(2026, 11, 23, 0, 5, 0, 0, time.UTC)
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.now = time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	before := f.model.rollupCalls()
	f.addHour(t, 22, 10, "More work later in the day.")
	f.model.output = "- Second pass."
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.model.rollupCalls() != before+1 || !strings.Contains(f.repo.rollup("day:2026-11-22").Text, "Second pass") {
		t.Fatal("a generated, dirty day was not regenerated when its hours changed")
	}
}

func TestRollupWorkerRegeneratesOnlyWhenInputHashChanges(t *testing.T) {
	f := newRollupFixture(t, power.LocalProvider, time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC))
	f.addHour(t, 15, 9, "Reviewed the migration plan.")
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := f.model.rollupCalls()
	hash := f.repo.rollup("day:2026-11-15").InputHash
	// The hour was rewritten with identical text: dirty, but no model call.
	f.repo.markDirty("day:2026-11-15")
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	day := f.repo.rollup("day:2026-11-15")
	if f.model.rollupCalls() != calls || day.Dirty || day.InputHash != hash {
		t.Fatalf("unchanged input regenerated: calls %d->%d dirty=%t", calls, f.model.rollupCalls(), day.Dirty)
	}
	// Changed hour text changes the hash and regenerates the day and its week.
	f.addHour(t, 15, 9, "Reviewed the migration plan and the rollback plan.")
	f.model.output = "- Reviewed both plans."
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	day = f.repo.rollup("day:2026-11-15")
	if f.model.rollupCalls() != calls+2 || day.InputHash == hash || !strings.Contains(day.Text, "both plans") || f.repo.rollup("week:2026-W46").Dirty {
		t.Fatalf("changed input not regenerated: calls=%d day=%+v", f.model.rollupCalls(), day)
	}
}

func TestRollupWorkerIsANoOpWithoutALocalProvider(t *testing.T) {
	f := newRollupFixture(t, power.RemoteProvider, time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC))
	f.addHour(t, 15, 9, "Reviewed the migration plan.")
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.model.rollupCalls() != 0 || f.repo.rollup("day:2026-11-15").Text != "" || !f.repo.rollup("day:2026-11-15").Dirty {
		t.Fatal("a remote or absent local provider generated rollups")
	}
	if status := f.worker.Status(); status.RollupReason != "skipped_no_local_provider" || status.State != "disabled" {
		t.Fatalf("status = %+v", status)
	}
	if RollupSkippedNoLocalProvider != "skipped_no_local_provider" {
		t.Fatal("status reason constant changed")
	}
}

func TestRollupWorkerDefersWeekUntilItsDaysAreGenerated(t *testing.T) {
	f := newRollupFixture(t, power.LocalProvider, time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC))
	f.addHour(t, 10, 9, "Monday work.")
	f.addHour(t, 12, 9, "Wednesday work.")
	f.model.output = "- Done."
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	week := f.repo.rollup("week:2026-W46")
	if week.Text == "" || len(week.ChildIDs) != 2 || week.ChildIDs[0] != "day:2026-11-10" || week.ChildIDs[1] != "day:2026-11-12" {
		t.Fatalf("week = %+v", week)
	}
	prompt := f.model.prompts[len(f.model.prompts)-1].User
	if !strings.Contains(prompt, "Tue 2026-11-10: - Done.") {
		t.Fatalf("week prompt does not label days: %q", prompt)
	}
	// A running week is never summarized.
	f.now = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	f.addHour(t, 17, 9, "Tuesday of the next week.")
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.repo.rollup("day:2026-11-17").Text == "" {
		t.Fatal("the ended day inside a running week was not summarized")
	}
	if running := f.repo.rollup("week:2026-W47"); running.Text != "" || !running.Dirty {
		t.Fatalf("a running week was summarized: %+v", running)
	}
}

func TestRollupWorkerRejectsInvalidModelOutput(t *testing.T) {
	f := newRollupFixture(t, power.LocalProvider, time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC))
	f.addHour(t, 15, 9, "Reviewed the migration plan.")
	f.model.output = "- see src:" + rollupSourceID(1)
	if err := f.worker.ProcessRollups(context.Background()); err == nil {
		t.Fatal("identifier-bearing output was accepted")
	}
	if f.repo.rollup("day:2026-11-15").Text != "" || f.worker.Status().LastErrorCode != "invalid_output" {
		t.Fatalf("invalid output was stored or misclassified: %+v", f.worker.Status())
	}
	if status := f.worker.Status(); status.RollupsPending != 1 || status.RollupsDirty != 1 {
		t.Fatalf("a failed rollup should stay pending: %+v", status)
	}
}

func TestRollupWorkerBackfillsHoursSavedBeforeRollupsExisted(t *testing.T) {
	f := newRollupFixture(t, power.LocalProvider, time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC))
	start := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	if err := f.repo.PutChronicle(context.Background(), Summary{HourID: "hour:20261114T09Z", Start: start, End: start.Add(time.Hour), Text: "Old hour.", SourceIDs: []string{rollupSourceID(7)}}); err != nil {
		t.Fatal(err)
	}
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if day := f.repo.rollup("day:2026-11-14"); day.Text == "" || len(day.SourceIDs) != 1 {
		t.Fatalf("pre-existing hour was never rolled up: %+v", day)
	}
}
