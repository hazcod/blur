package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/chronicle"
)

// rollupChronicles serves hour recaps and rollups from memory by range.
type rollupChronicles struct {
	testChronicles
	hours   []chronicle.Summary
	rollups []chronicle.Rollup
}

func (r rollupChronicles) ListChronicles(_ context.Context, start, end time.Time, _ string, _ int) ([]chronicle.Summary, string, error) {
	var out []chronicle.Summary
	for _, row := range r.hours {
		if !row.Start.Before(start) && row.Start.Before(end) {
			out = append(out, row)
		}
	}
	return out, "", nil
}

func (r rollupChronicles) ListChronicleRollups(_ context.Context, level string, start, end time.Time, _ string, _ int) ([]chronicle.Rollup, string, error) {
	var out []chronicle.Rollup
	for _, row := range r.rollups {
		if row.Level == level && !row.Start.Before(start) && row.Start.Before(end) {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, "", nil
}

func rollupFixtureSource(n int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("rollup-source-%d", n)))
	return hex.EncodeToString(sum[:])
}

func rollupFixtureRecord(t *testing.T, id, text string, dirty bool, sources ...string) chronicle.Rollup {
	t.Helper()
	row, err := chronicle.NewRollupPlaceholder(id, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	row.Dirty = dirty
	if text == "" {
		return row
	}
	sort.Strings(sources)
	sum := sha256.Sum256([]byte(text))
	row.Text, row.SourceIDs, row.InputHash = text, sources, hex.EncodeToString(sum[:])
	row.ChildIDs = []string{"hour:20261110T09Z"}
	if row.Level == "week" {
		row.ChildIDs = []string{"day:2026-11-10"}
	}
	row.GeneratedAt, row.Model = time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC), "local-test-model"
	return row
}

func hourRecapAt(day, hour int, text string, source string) chronicle.Summary {
	start := time.Date(2026, 11, day, hour, 0, 0, 0, time.UTC)
	return chronicle.Summary{HourID: "hour:" + start.Format("20060102T15Z"), Start: start, End: start.Add(time.Hour), Text: text, SourceIDs: []string{source}}
}

func layerCount(items []assistant.Evidence, layer assistant.Layer) int {
	n := 0
	for _, item := range items {
		if item.Layer == layer {
			n++
		}
	}
	return n
}

func TestRecapPrefersDayRollupForYesterdayAndHoursForThisAfternoon(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	source := rollupFixtureSource(1)
	other := rollupFixtureSource(2)
	f.client.chronicles = rollupChronicles{
		hours: []chronicle.Summary{
			hourRecapAt(10, 9, "Hourly: reviewed the proposal.", source),
			hourRecapAt(10, 14, "Hourly: fixed the release build.", other),
			hourRecapAt(11, 12, "Hourly: afternoon planning session.", rollupFixtureSource(3)),
			hourRecapAt(11, 13, "Hourly: afternoon code review.", rollupFixtureSource(4)),
		},
		rollups: []chronicle.Rollup{
			rollupFixtureRecord(t, "day:2026-11-10", "- Reviewed the proposal and fixed the release build.", false, source, other),
			rollupFixtureRecord(t, "day:2026-11-11", "- A partial day that must not be used for this afternoon.", false, source),
		},
	}
	yesterday := assistant.Plan{Kind: assistant.KindRecap, Scope: assistant.Scope{Start: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), Label: "yesterday", Explicit: true}}
	got, err := f.client.RetrievePlan(context.Background(), yesterday)
	if err != nil {
		t.Fatal(err)
	}
	if layerCount(got.Evidence, assistant.LayerDay) != 1 {
		t.Fatalf("yesterday did not use its day rollup: %+v", got.Evidence)
	}
	if got.Evidence[0].Layer != assistant.LayerDay {
		t.Fatalf("the day rollup does not lead the evidence: %+v", got.Evidence)
	}
	day := got.Evidence[0]
	wantID := syntheticID("chronicle-rollup", "day:2026-11-10", time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano), time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano), day.Text)
	if day.SourceID != wantID || len(day.SourceIDs) != 2 || day.SourceIDs[0] > day.SourceIDs[1] || day.Text != "- Reviewed the proposal and fixed the release build." {
		t.Fatalf("day rollup evidence lacks provenance or the synthetic ID: %+v", day)
	}
	if layerCount(got.Evidence, assistant.LayerHour) != 0 {
		t.Fatalf("hour recaps repeated inside a day already covered by its rollup: %+v", got.Evidence)
	}

	afternoon := assistant.Plan{Kind: assistant.KindRecap, Scope: assistant.Scope{Start: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC), Label: "this afternoon", Explicit: true}}
	got, err = f.client.RetrievePlan(context.Background(), afternoon)
	if err != nil {
		t.Fatal(err)
	}
	if layerCount(got.Evidence, assistant.LayerDay) != 0 || layerCount(got.Evidence, assistant.LayerWeek) != 0 || layerCount(got.Evidence, assistant.LayerHour) != 2 {
		t.Fatalf("this afternoon must use hour recaps, not a day rollup: %+v", got.Evidence)
	}

	// Today so far is not a whole day either.
	got, err = f.client.RetrievePlan(context.Background(), todayPlan(assistant.KindRecap))
	if err != nil || layerCount(got.Evidence, assistant.LayerDay) != 0 {
		t.Fatalf("an unfinished day used its rollup: %+v err=%v", got.Evidence, err)
	}

	// A filtered recap never reads rollups.
	filtered := yesterday
	filtered.AppIDs = []string{waApp}
	got, err = f.client.RetrievePlan(context.Background(), filtered)
	if err != nil || layerCount(got.Evidence, assistant.LayerDay) != 0 {
		t.Fatalf("app-filtered recap used a rollup: %+v err=%v", got.Evidence, err)
	}
}

func TestRecapUsesWeekAndDayRollupsAndReportsDirty(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	src := rollupFixtureSource(1)
	f.client.chronicles = rollupChronicles{
		hours: []chronicle.Summary{hourRecapAt(2, 10, "Hourly: monday detail.", src)},
		rollups: []chronicle.Rollup{
			rollupFixtureRecord(t, "week:2026-W45", "- A week of release work.", false, src),
			rollupFixtureRecord(t, "day:2026-11-02", "- Monday release work.", false, src),
			rollupFixtureRecord(t, "day:2026-11-03", "", true),
			rollupFixtureRecord(t, "day:2026-11-04", "- Wednesday review.", true, src),
		},
	}
	week := assistant.Plan{Kind: assistant.KindRecap, Scope: assistant.Scope{Start: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), Label: "last week", Explicit: true}}
	got, err := f.client.RetrievePlan(context.Background(), week)
	if err != nil {
		t.Fatal(err)
	}
	if layerCount(got.Evidence, assistant.LayerWeek) != 1 || layerCount(got.Evidence, assistant.LayerDay) != 2 || got.Evidence[0].Layer != assistant.LayerWeek {
		t.Fatalf("week and day rollups not selected week-first: %+v", got.Evidence)
	}
	if got.Diagnosis.RollupsDirty != 2 {
		t.Fatalf("RollupsDirty = %d, want 2 (a placeholder and a stale day)", got.Diagnosis.RollupsDirty)
	}
	for _, item := range got.Evidence {
		if item.Layer == assistant.LayerHour {
			t.Fatalf("hour recap repeated inside a covered day: %+v", item)
		}
	}
	// A cited rollup can be reloaded by its synthetic ID for a follow-up.
	followup, err := f.client.RetrieveSources(context.Background(), week, []string{got.Evidence[0].SourceID})
	if err != nil || len(followup.Evidence) != 1 || followup.Evidence[0].SourceID != got.Evidence[0].SourceID {
		t.Fatalf("a cited rollup could not be reloaded: %+v err=%v", followup, err)
	}
}

func TestRecapIgnoresRollupsBuiltForAnotherZoneOrPartialPeriod(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	src := rollupFixtureSource(1)
	other, err := chronicle.NewRollupPlaceholder("day:2026-11-10", "Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	other.Text, other.SourceIDs, other.ChildIDs = "- Built for another zone.", []string{src}, []string{"hour:20261110T09Z"}
	other.InputHash, other.GeneratedAt, other.Dirty = strings.Repeat("a", 64), time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC), false
	f.client.chronicles = rollupChronicles{rollups: []chronicle.Rollup{other}}
	plan := assistant.Plan{Kind: assistant.KindRecap, Scope: assistant.Scope{Start: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), Explicit: true}}
	got, err := f.client.RetrievePlan(context.Background(), plan)
	if err != nil || layerCount(got.Evidence, assistant.LayerDay) != 0 {
		t.Fatalf("a rollup for different bounds was used: %+v err=%v", got.Evidence, err)
	}
}

type failingRollupChronicles struct{ rollupChronicles }

func (failingRollupChronicles) ListChronicleRollups(context.Context, string, time.Time, time.Time, string, int) ([]chronicle.Rollup, string, error) {
	return nil, "", errors.New("rollup read failed")
}

func TestRecapFailsClosedWhenRollupReadFails(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	f.client.chronicles = failingRollupChronicles{}
	plan := assistant.Plan{Kind: assistant.KindRecap, Scope: assistant.Scope{Start: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), Explicit: true}}
	if _, err := f.client.RetrievePlan(context.Background(), plan); err == nil {
		t.Fatal("a rollup read failure was swallowed")
	}
}
