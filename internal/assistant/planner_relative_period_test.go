package assistant

import (
	"slices"
	"testing"
	"time"
)

func TestRelativeSearchPeriodsCoverLargeHistories(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 22, 15, 10, 0, 0, zone)
	for _, tc := range []struct {
		period string
		start  time.Time
	}{
		{"3 years", now.AddDate(-3, 0, 0)},
		{"24 months", now.AddDate(0, -24, 0)},
		{"104 weeks", now.AddDate(0, 0, -728)},
		{"1000 days", now.AddDate(0, 0, -1000)},
		{"20000 hours", now.Add(-20000 * time.Hour)},
		{"2000000 minutes", now.Add(-2000000 * time.Minute)},
	} {
		for _, prefix := range []string{"last", "past"} {
			question := "Find parser validation in the " + prefix + " " + tc.period
			p, err := PlanQuestion(question, now, nil)
			if err != nil || !p.Scope.Explicit || !p.Scope.Start.Equal(tc.start) || !p.Scope.End.Equal(now) {
				t.Errorf("lost requested history %q: scope=%+v err=%v", question, p.Scope, err)
			}
			if !slices.Equal(p.Terms, []string{"parser", "validation"}) {
				t.Errorf("period contaminated search terms %q: %v", question, p.Terms)
			}
		}
	}
}

func TestRelativeSearchPeriodPreservesRepeatedTopicNumber(t *testing.T) {
	now := time.Date(2026, 11, 22, 15, 0, 0, 0, time.UTC)
	p, err := PlanQuestion("Find project 3 in the last 3 years", now, nil)
	if err != nil || !p.Scope.Start.Equal(now.AddDate(-3, 0, 0)) || !slices.Contains(p.Terms, "3") || slices.Contains(p.Terms, "year") {
		t.Fatalf("relative date erased topic identity: %+v %v", p, err)
	}
}

func TestRelativeSearchPeriodDoesNotOverflowIntoRecentTime(t *testing.T) {
	now := time.Date(2026, 11, 22, 15, 0, 0, 0, time.UTC)
	for _, unit := range []string{"years", "months", "weeks", "days", "hours", "minutes"} {
		p, err := PlanQuestion("Find parser in the last 999999999999999999999999 "+unit, now, nil)
		if err != nil || !p.Scope.Explicit || !p.Scope.Start.Equal(time.Unix(0, -1<<63)) || !p.Scope.End.Equal(now) {
			t.Errorf("unbounded numeric period lost oldest representable date %s: %+v %v", unit, p.Scope, err)
		}
	}
}
