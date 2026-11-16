package chronicle_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

type durablePolicy struct{}

func (durablePolicy) Effective(context.Context) (power.Effective, error) {
	return power.Resolve(power.Config{Mode: power.Balanced}, power.SystemState{})
}

// durableGenerator is a fake local generator. onGenerate runs once, in the
// middle of the first rollup generation that follows it being set.
type durableGenerator struct {
	calls      int
	prompts    []string
	onGenerate func()
}

func (g *durableGenerator) CountInputTokens(context.Context, chronicle.Packed) (int, error) {
	return 10, nil
}
func (g *durableGenerator) Generate(context.Context, chronicle.Packed, int) (chronicle.Summary, error) {
	return chronicle.Summary{}, chronicle.ErrInvalid
}
func (g *durableGenerator) CountRollupTokens(context.Context, chronicle.RollupPrompt) (int, error) {
	return 100, nil
}
func (g *durableGenerator) GenerateRollup(_ context.Context, p chronicle.RollupPrompt, _ int) (string, error) {
	g.calls++
	g.prompts = append(g.prompts, p.User)
	if hook := g.onGenerate; hook != nil {
		g.onGenerate = nil
		hook()
	}
	return "- Worked through the day.", nil
}

type durableRollupFixture struct {
	t      *testing.T
	store  *durable.Store
	worker *chronicle.Worker
	gen    *durableGenerator
	now    time.Time
}

func newDurableRollupFixture(t *testing.T) *durableRollupFixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := bolt.CreatePrivateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	store, err := durable.OpenDurable(filepath.Join(dir, "store.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetChronicleZone(func() string { return "UTC" })
	f := &durableRollupFixture{t: t, store: store, gen: &durableGenerator{}, now: time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)}
	f.worker = chronicle.NewWorker(store, durablePolicy{}, f.gen, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider, Now: func() time.Time { return f.now }, Zone: func() string { return "UTC" }})
	t.Cleanup(f.worker.Close)
	return f
}

func (f *durableRollupFixture) capture(text string, at time.Time) string {
	f.t.Helper()
	observation, err := capture.SanitizeObservation("editor.app", "", text, 1024)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.PutObservationAt(context.Background(), observation, at); err != nil {
		f.t.Fatal(err)
	}
	return observation.ContentID()
}

// hour saves an hour recap that cites one capture. extraInput, when set, is
// an additional capture that fed the recap without being cited.
func (f *durableRollupFixture) hour(day, hour int, text string, extraInput string) (cited, hourID string) {
	f.t.Helper()
	start := time.Date(2026, 11, day, hour, 0, 0, 0, time.UTC)
	cited = f.capture("cited capture for "+text, start.Add(time.Minute))
	summary := chronicle.Summary{HourID: "hour:" + start.Format("20060102T15Z"), Start: start, End: start.Add(time.Hour), Text: text, SourceIDs: []string{cited}}
	if extraInput != "" {
		input := []string{cited, extraInput}
		if input[0] > input[1] {
			input[0], input[1] = input[1], input[0]
		}
		summary.InputSourceIDs = input
	}
	if err := f.store.PutChronicle(context.Background(), summary); err != nil {
		f.t.Fatal(err)
	}
	return cited, summary.HourID
}

func (f *durableRollupFixture) process() {
	f.t.Helper()
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *durableRollupFixture) rollup(id string) chronicle.Rollup {
	f.t.Helper()
	r, _, err := f.store.GetChronicleRollup(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func TestDurableWorkerGeneratesDayThenWeekFromStoredHours(t *testing.T) {
	f := newDurableRollupFixture(t)
	f.hour(15, 9, "Reviewed the migration plan.", "")
	f.hour(15, 14, "Wrote the rollout notes.", "")
	f.process()
	day := f.rollup("day:2026-11-15")
	if day.Text == "" || day.Dirty || len(day.ChildIDs) != 2 || len(day.SourceIDs) != 2 {
		t.Fatalf("day = %+v", day)
	}
	if week := f.rollup("week:2026-W46"); week.Text == "" || week.Dirty || week.ChildIDs[0] != "day:2026-11-15" {
		t.Fatalf("week = %+v", week)
	}
	// Rewriting an hour with identical text marks the day dirty but costs no
	// second model call.
	calls := f.gen.calls
	existing, _, err := f.store.GetChronicle(context.Background(), "hour:20261115T14Z")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.PutChronicle(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	f.process()
	if f.gen.calls != calls || f.rollup("day:2026-11-15").Dirty {
		t.Fatalf("unchanged hours regenerated: calls %d->%d", calls, f.gen.calls)
	}
}

func TestDurableWorkerKeepsDirtyWhenAnHourArrivesDuringGeneration(t *testing.T) {
	f := newDurableRollupFixture(t)
	f.hour(15, 9, "Morning review.", "")
	f.gen.onGenerate = func() { f.hour(15, 23, "Late evening hour saved mid-generation.", "") }
	f.process()
	day := f.rollup("day:2026-11-15")
	if !day.Dirty || len(day.ChildIDs) != 1 {
		t.Fatalf("a recap written during generation was lost: %+v", day)
	}
	f.process()
	day = f.rollup("day:2026-11-15")
	if day.Dirty || len(day.ChildIDs) != 2 {
		t.Fatalf("the late hour was not picked up on the next pass: %+v", day)
	}
	if week := f.rollup("week:2026-W46"); week.Dirty || len(week.SourceIDs) != 2 {
		t.Fatalf("week did not follow the regenerated day: %+v", week)
	}
}

func TestDurableErasureOfInputOnlySourceClearsDayAndWeekText(t *testing.T) {
	f := newDurableRollupFixture(t)
	private := f.capture("private capture that only fed an hour recap", time.Date(2026, 11, 15, 9, 2, 0, 0, time.UTC))
	_, hourID := f.hour(15, 9, "Hour built partly from a private capture.", private)
	f.hour(15, 14, "Unrelated hour.", "")
	f.process()
	if f.rollup("day:2026-11-15").Text == "" || f.rollup("week:2026-W46").Text == "" {
		t.Fatal("setup: day and week were not generated")
	}
	if err := f.store.DeleteObservationRecord(context.Background(), private); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := f.store.GetChronicle(context.Background(), hourID); found {
		t.Fatal("the hour recap survived the erasure")
	}
	for _, id := range []string{"day:2026-11-15", "week:2026-W46"} {
		if r := f.rollup(id); r.Text != "" || !r.Dirty || r.InputHash != "" || len(r.SourceIDs) != 0 {
			t.Fatalf("%s still serves text derived from an erased input-only source: %+v", id, r)
		}
	}
	// The next pass rebuilds both from the hour that remains.
	f.process()
	day, week := f.rollup("day:2026-11-15"), f.rollup("week:2026-W46")
	if day.Text == "" || day.Dirty || len(day.ChildIDs) != 1 || week.Text == "" || week.Dirty {
		t.Fatalf("rollups did not regenerate from the remaining hour: day=%+v week=%+v", day, week)
	}
}
