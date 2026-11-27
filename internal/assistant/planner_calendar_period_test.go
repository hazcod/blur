package assistant

import (
	"testing"
	"time"
)

func TestCalendarMonthAndYearQuestionsUseTheirActualPeriod(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ question, start, end string }{
		{"Summarize last month", "2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z"},
		{"Summarize this month", "2026-11-01T00:00:00Z", "2026-11-21T15:00:00Z"},
		{"Summarize last year", "2025-01-01T00:00:00Z", "2026-01-01T00:00:00Z"},
		{"Summarize this year", "2026-01-01T00:00:00Z", "2026-11-21T15:00:00Z"},
		{"Summarize my activity in October 2026", "2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z"},
		{"Summarize my activity in 2024", "2024-01-01T00:00:00Z", "2025-01-01T00:00:00Z"},
	} {
		p, err := PlanQuestion(tc.question, now, nil)
		if err != nil || p.Kind != KindRecap || p.Scope.Start.Format(time.RFC3339) != tc.start || p.Scope.End.Format(time.RFC3339) != tc.end {
			t.Errorf("calendar question lost its real period: %q %+v %v", tc.question, p, err)
		}
	}
	for _, question := range []string{"Find project 2025", "What time is my appointment in October 2026?"} {
		p, err := PlanQuestion(question, now, nil)
		if err != nil || !p.Scope.IsZero() {
			t.Errorf("topic/event date became observation period: %q %+v %v", question, p, err)
		}
	}
}
