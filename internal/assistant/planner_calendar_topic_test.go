package assistant

import (
	"slices"
	"testing"
	"time"
)

func TestReviewCalendarScopePreservesSharedTopicNumber(t *testing.T) {
	p, err := PlanQuestion("Find project 2026 in November 2026", time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC), nil)
	if err != nil || !slices.Contains(p.Terms, "2026") || !slices.Contains(p.NumberedSubjects, [2]string{"project", "2026"}) {
		t.Fatalf("date consumed another occurrence belonging to the requested subject: %+v %v", p, err)
	}
}

func TestIndependentReviewCalendarActivityClausePreservesRootTopicNumber(t *testing.T) {
	p, err := PlanQuestion("What time is my Project 2026 appointment in the email I read in November 2026?", time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC), nil)
	if err != nil || !slices.Contains(p.Terms, "2026") || !slices.Contains(p.NumberedSubjects, [2]string{"project", "2026"}) {
		t.Fatalf("capture-date view erased root event topic: %+v %v", p, err)
	}
}

func TestCalendarScopePreservesPossessiveMonthNamedTopics(t *testing.T) {
	p, err := PlanQuestion("Find May’s invoice in May 2026", time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC), nil)
	if err != nil || !slices.Contains(p.Terms, "may") || !slices.Contains(p.Terms, "invoice") {
		t.Fatalf("calendar date erased a folded topic: %+v %v", p, err)
	}
}

func TestCalendarScopeIgnoresDomainsAndQuotedMonthTopicsInNativeAsk(t *testing.T) {
	for _, q := range []string{"Summarize my activity in may.example", `Find the report for "May 2026"`} {
		p, err := PlanQuestion(q, time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC), nil)
		if err != nil || !p.Scope.IsZero() {
			t.Fatalf("domain/literal became a capture period: %q %+v %v", q, p, err)
		}
	}
}

func TestMonthPeriodGrammarDoesNotBroadenSpecificDayPhrases(t *testing.T) {
	for _, q := range []string{"Find invoice 23 in November 23", "Find invoice 23 in November 23rd"} {
		p, err := PlanQuestion(q, time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC), nil)
		if err != nil || !p.Scope.IsZero() {
			t.Fatalf("specific day became a whole month: %q %+v %v", q, p, err)
		}
	}
}
