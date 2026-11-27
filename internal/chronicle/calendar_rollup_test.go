package chronicle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/power"
)

func TestCalendarRollupPeriodsRespectLeapYearsAndLocalMidnight(t *testing.T) {
	for _, tc := range []struct{ id, zone, start, end string }{
		{"month:2024-02", "UTC", "2024-02-01T00:00:00Z", "2024-03-01T00:00:00Z"},
		{"year:2024", "UTC", "2024-01-01T00:00:00Z", "2025-01-01T00:00:00Z"},
		{"month:2026-03", "Europe/Brussels", "2026-02-28T23:00:00Z", "2026-03-31T22:00:00Z"},
		{"month:2026-10", "Europe/Brussels", "2026-09-30T22:00:00Z", "2026-10-31T23:00:00Z"},
		{"year:2026", "Pacific/Auckland", "2025-12-31T11:00:00Z", "2026-12-31T11:00:00Z"},
	} {
		r, err := NewRollupPlaceholder(tc.id, tc.zone)
		if err != nil || r.Start.Format(time.RFC3339) != tc.start || r.End.Format(time.RFC3339) != tc.end || ValidateRollup(r) != nil {
			t.Errorf("calendar period lost its actual bounds: %s %+v %v", tc.id, r, err)
		}
	}
	for _, id := range []string{"month:2026-13", "month:2026-00", "month:2026-1", "year:202x", "year:0000", "year:26"} {
		if _, err := NewRollupPlaceholder(id, "UTC"); err == nil {
			t.Errorf("invalid calendar period accepted: %s", id)
		}
	}
}

func TestCalendarRollupChildrenAndPromptsUseTheCorrectHierarchy(t *testing.T) {
	for _, tc := range []struct{ id, child, invalid string }{
		{"month:2026-11", "day:2026-11-15", "week:2026-W46"},
		{"year:2026", "month:2026-11", "day:2026-11-15"},
	} {
		r, err := NewRollupPlaceholder(tc.id, "UTC")
		if err != nil {
			t.Error(err)
			continue
		}
		r.Text, r.Model, r.GeneratedAt = "- Reviewed the release evidence.", "local-test-model", time.Date(2027, 1, 1, 1, 0, 0, 0, time.UTC)
		r.SourceIDs, r.ChildIDs, r.InputHash = []string{strings.Repeat("a", 64)}, []string{tc.child}, strings.Repeat("b", 64)
		if ValidateRollup(r) != nil {
			t.Errorf("correct calendar hierarchy rejected: %+v", r)
		}
		input := NewRollupInput(r.Level, []RollupChild{{ID: tc.child, Label: "Observed period", Text: r.Text, SourceIDs: r.SourceIDs}})
		prompt, err := BuildRollupPrompt(input, tc.id)
		if err != nil || !strings.Contains(prompt.System, r.Level) || !strings.Contains(prompt.User, r.Text) || len(prompt.User) > MaxRollupInputBytes+128 {
			t.Errorf("calendar input did not reach a bounded matching prompt: %+v %v", prompt, err)
		}
		r.ChildIDs = []string{tc.invalid}
		if ValidateRollup(r) == nil {
			t.Errorf("wrong calendar child hierarchy accepted: %+v", r)
		}
	}
}

func TestRollupInputReceiptChangesWithIdentityAndProvenance(t *testing.T) {
	child := RollupChild{ID: "day:2026-11-15", Label: "15 November", Text: "Reviewed the release.", SourceIDs: []string{strings.Repeat("a", 64)}}
	base := NewRollupInput("week", []RollupChild{child}).Hash()
	for _, change := range []string{"id", "label", "source"} {
		other := child
		switch change {
		case "id":
			other.ID = "day:2026-11-16"
		case "label":
			other.Label = "16 November"
		case "source":
			other.SourceIDs = []string{strings.Repeat("b", 64)}
		}
		if got := NewRollupInput("week", []RollupChild{other}).Hash(); got == base {
			t.Errorf("changed %s reused a stale input receipt", change)
		}
	}
}

func TestCalendarRollupInputRepresentsTheWholePeriodWithinItsBudget(t *testing.T) {
	for _, level := range []string{"month", "year"} {
		var children []RollupChild
		for i := 0; i < 31; i++ {
			children = append(children, RollupChild{ID: time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC).Format("day:2006-01-02"), Label: "observed day", Text: strings.Repeat("é", 1000), SourceIDs: []string{strings.Repeat("a", 64)}})
		}
		in := NewRollupInput(level, children)
		prompt, err := BuildRollupPrompt(in, "calendar period")
		if err != nil || len(in.Children) != len(children) || !in.Omitted || len(prompt.User) > MaxRollupInputBytes+128 {
			t.Fatalf("calendar input silently kept only the end of the period: level=%s children=%d omitted=%v bytes=%d err=%v", level, len(in.Children), in.Omitted, len(prompt.User), err)
		}
		if in.Children[0].ID != children[0].ID || in.Children[len(in.Children)-1].ID != children[len(children)-1].ID {
			t.Fatal("calendar boundaries lost")
		}
	}
}

func TestRollupWorkerSameTextWithNewSourceUpdatesProvenance(t *testing.T) {
	f := newRollupFixture(t, power.LocalProvider, time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC))
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64)
	f.addHour(t, 15, 9, "Reviewed the release.", first)
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if day := f.repo.rollup("day:2026-11-15"); len(day.SourceIDs) != 1 || day.SourceIDs[0] != first {
		t.Fatal("fixture failed before provenance replacement", day)
	}
	f.addHour(t, 15, 9, "Reviewed the release.", second)
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	day := f.repo.rollup("day:2026-11-15")
	if len(day.SourceIDs) != 1 || day.SourceIDs[0] != second {
		t.Fatalf("same text retained the old source as provenance: %+v", day)
	}
}

func TestRollupWorkerPersistsAndPropagatesSampledCoverage(t *testing.T) {
	f := newRollupFixture(t, power.LocalProvider, time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC))
	for hour := 9; hour < 17; hour++ {
		f.addHour(t, 15, hour, strings.Repeat("Reviewed launch evidence. ", 70), rollupSourceID(hour))
	}
	if err := f.worker.ProcessRollups(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"day:2026-11-15", "week:2026-W46"} {
		row := f.repo.rollup(id)
		if row.Text == "" || !row.Sampled || row.CoverageVersion != 1 {
			t.Fatalf("coverage was lost at %s: %+v", id, row)
		}
	}
}
