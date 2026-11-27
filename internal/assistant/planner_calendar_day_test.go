package assistant

import (
	"slices"
	"testing"
	"time"
)

func TestExplicitCalendarDayFiltersObservations(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, loc)
	for _, question := range []string{
		"What did I say in my voice note on November 1 2026 about Cedar Lantern?",
		"Find Cedar Lantern on 1 November 2026",
		"Find Cedar Lantern on November 1st, 2026",
		"Find Cedar Lantern on Sunday 1 November 2026",
	} {
		for _, planner := range []func(string, time.Time, []KnownApp) (Plan, error){PlanQuestion, PlanSearch} {
			p, err := planner(question, now, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !p.Scope.Start.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, loc)) || !p.Scope.End.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, loc)) || !p.Scope.Explicit {
				t.Errorf("explicit capture date was not a local full day: %q %+v", question, p.Scope)
			}
			for _, term := range []string{"november", "1", "1st", "2026", "sunday"} {
				if slices.Contains(p.Terms, term) {
					t.Errorf("capture date became a required content term: %q %+v", question, p.Terms)
				}
			}
			if !slices.Contains(p.Terms, "cedar") || !slices.Contains(p.Terms, "lantern") {
				t.Errorf("capture date removed the topic: %q %+v", question, p.Terms)
			}
		}
	}
}

func TestExplicitCalendarDayRespectsDSTAndDoesNotClampToNow(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, loc)
	for _, tc := range []struct {
		question   string
		start, end string
		hours      time.Duration
	}{
		{"Find Cedar Lantern on March 29 2026", "2026-03-29T00:00:00+01:00", "2026-03-30T00:00:00+02:00", 23},
		{"Find Cedar Lantern on October 25 2026", "2026-10-25T00:00:00+02:00", "2026-10-26T00:00:00+01:00", 25},
		{"Find Cedar Lantern on November 22 2026", "2026-11-22T00:00:00+01:00", "2026-11-23T00:00:00+01:00", 24},
		{"Find Cedar Lantern on December 1 2027", "2027-12-01T00:00:00+01:00", "2027-12-02T00:00:00+01:00", 24},
	} {
		p, err := PlanSearch(tc.question, now, nil)
		if err != nil || p.Scope.Start.Format(time.RFC3339) != tc.start || p.Scope.End.Format(time.RFC3339) != tc.end || p.Scope.End.Sub(p.Scope.Start) != tc.hours*time.Hour {
			t.Errorf("calendar day used the wrong year, timezone, or end boundary: %q %+v %v", tc.question, p.Scope, err)
		}
	}
}

func TestExplicitCalendarDayPreservesRepeatedTopicWords(t *testing.T) {
	for _, tc := range []struct {
		question string
		terms    []string
	}{
		{"Find invoice 1 on November 1 2026", []string{"invoice", "1"}},
		{"Find project 2026 on November 1 2026", []string{"project", "2026"}},
		{"Find May's invoice on May 1 2026", []string{"may", "invoice"}},
	} {
		p, err := PlanQuestion(tc.question, time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC), nil)
		if err != nil || p.Scope.IsZero() {
			t.Errorf("capture date was not recognized: %q %+v %v", tc.question, p, err)
		}
		for _, term := range tc.terms {
			if !slices.Contains(p.Terms, term) {
				t.Errorf("capture date erased a separate subject occurrence: %q %+v", tc.question, p.Terms)
			}
		}
	}
}

func TestExplicitCalendarDayKeepsEventDatesAsContent(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	for _, planner := range []func(string, time.Time, []KnownApp) (Plan, error){PlanQuestion, PlanSearch} {
		p, err := planner("What time is my Cedar Lantern appointment on November 1 2026?", now, nil)
		if err != nil || !p.Scope.IsZero() || !slices.Contains(p.Terms, "november") || !slices.Contains(p.Terms, "2026") {
			t.Fatalf("event date became a capture filter: %+v %v", p, err)
		}
		p, err = planner("What time is my appointment on November 1 2026 from the email I read on November 2 2026?", now, nil)
		if err != nil || !p.Scope.Start.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)) || !slices.Contains(p.Terms, "1") || slices.Contains(p.Terms, "2") {
			t.Fatalf("event and capture date were confused: %+v %v", p, err)
		}
	}
}

func TestExplicitCalendarDayIgnoresTopicsAndInvalidDates(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	for _, question := range []string{
		`Find "November 1 2026"`,
		`Find the report on "November 1 2026"`,
		"Find invoice 23 in November 23",
		"Find project 2026",
		"Find Cedar Lantern on February 29 2026",
		"Find Cedar Lantern on April 31 2026",
		"Find Cedar Lantern on november1.example",
	} {
		for _, planner := range []func(string, time.Time, []KnownApp) (Plan, error){PlanQuestion, PlanSearch} {
			p, err := planner(question, now, nil)
			if err != nil || !p.Scope.IsZero() {
				t.Errorf("topic or invalid date became a capture filter: %q %+v %v", question, p.Scope, err)
			}
		}
	}
}

func TestVoiceNoteAppAliasScopesRecallToRecordedSpeech(t *testing.T) {
	for _, question := range []string{"silver maple in Voice notes", "silver maple in Voice note"} {
		for _, planner := range []func(string, time.Time, []KnownApp) (Plan, error){PlanQuestion, PlanSearch} {
			p, err := planner(question, time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC), nil)
			if err != nil || !slices.Equal(p.AppIDs, []string{"sh.blur.meetings"}) || !slices.Equal(p.Terms, []string{"maple", "silver"}) {
				t.Errorf("voice alias did not constrain speech evidence: %q %+v %v", question, p, err)
			}
		}
	}
}

func TestExplicitCalendarDayPreservesDayParts(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, loc)
	for _, tc := range []struct {
		question   string
		start, end string
	}{
		{"Find Cedar on November 1 2025 morning", "2025-11-01T06:00:00+01:00", "2025-11-01T12:00:00+01:00"},
		{"Find Cedar on 1 November 2025 afternoon", "2025-11-01T12:00:00+01:00", "2025-11-01T18:00:00+01:00"},
		{"Find Cedar on November 1 2025 in the evening", "2025-11-01T18:00:00+01:00", "2025-11-02T00:00:00+01:00"},
		{"Find Cedar on the morning of November 1 2025", "2025-11-01T06:00:00+01:00", "2025-11-01T12:00:00+01:00"},
		{"Find Cedar on 29 March 2026 morning", "2026-03-29T06:00:00+02:00", "2026-03-29T12:00:00+02:00"},
		{"Find Cedar on October 25 2026 evening", "2026-10-25T18:00:00+01:00", "2026-10-26T00:00:00+01:00"},
		{"Find Cedar on March 28 2026 night", "2026-03-28T20:00:00+01:00", "2026-03-29T06:00:00+02:00"},
		{"Find Cedar on 24 October 2026 night", "2026-10-24T20:00:00+02:00", "2026-10-25T06:00:00+01:00"},
	} {
		for _, planner := range []func(string, time.Time, []KnownApp) (Plan, error){PlanQuestion, PlanSearch} {
			p, err := planner(tc.question, now, nil)
			if err != nil || p.Scope.Start.Format(time.RFC3339) != tc.start || p.Scope.End.Format(time.RFC3339) != tc.end {
				t.Errorf("explicit date lost its daypart or local clock bounds: %q %+v %v", tc.question, p.Scope, err)
			}
			if !slices.Equal(p.Terms, []string{"cedar"}) {
				t.Errorf("date or daypart became content: %q %+v", tc.question, p.Terms)
			}
		}
	}
}

func TestExplicitCalendarDayTakesPriorityOverBareWeekdayDayPart(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 22, 14, 0, 0, 0, loc)
	for _, tc := range []struct {
		question   string
		start, end string
	}{
		{"Find Cedar on Sunday morning, November 1 2026", "2026-11-01T06:00:00+01:00", "2026-11-01T12:00:00+01:00"},
		{"Find Cedar on Saturday afternoon, 1 November 2025", "2025-11-01T12:00:00+01:00", "2025-11-01T18:00:00+01:00"},
	} {
		for _, planner := range []func(string, time.Time, []KnownApp) (Plan, error){PlanQuestion, PlanSearch} {
			p, err := planner(tc.question, now, nil)
			if err != nil || p.Scope.Start.Format(time.RFC3339) != tc.start || p.Scope.End.Format(time.RFC3339) != tc.end {
				t.Errorf("bare weekday overrode the explicit date: %q %+v %v", tc.question, p.Scope, err)
			}
			if !slices.Equal(p.Terms, []string{"cedar"}) {
				t.Errorf("explicit date left a redundant weekday content filter: %q %+v", tc.question, p.Terms)
			}
		}
	}
}
