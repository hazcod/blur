package assistant

import (
	"slices"
	"testing"
	"time"
)

func TestPlannerGrammarMatchesInFoldedSpace(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		question string
		terms    []string
	}{
		{"résumer la page entière sur Atlas", []string{"atlas"}},
		{"resumer la page entiere sur Atlas", []string{"atlas"}},
		{"summarize whole page on Café Central", []string{"cafe", "central"}},
	} {
		t.Run(test.question, func(t *testing.T) {
			plan, err := PlanQuestion(test.question, now, nil)
			if err != nil || !plan.WholeDocument {
				t.Fatalf("plan = %+v, err=%v", plan, err)
			}
			if !slices.Equal(plan.Terms, test.terms) {
				t.Fatalf("terms = %v, want %v", plan.Terms, test.terms)
			}
		})
	}
}

func TestPlannerKeepsPossessiveEntity(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	plan, err := PlanQuestion("recap my convo with Dima's team", now, nil)
	if err != nil || plan.Entity != "Dima" || !slices.Equal(plan.Terms, []string{"dima"}) {
		t.Fatalf("possessive entity lost: %+v err=%v", plan, err)
	}
}
