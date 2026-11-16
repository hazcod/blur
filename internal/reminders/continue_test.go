package reminders

import (
	"testing"
	"time"
)

func TestReminderClarificationPreservesTitleDateAndOriginalRelativeIntent(t *testing.T) {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	draft, err := Parse("remind me tomorrow at 25 to review the PR", at, "Europe/Brussels")
	if err != nil || draft.Missing != "time" {
		t.Fatal(draft, err)
	}
	filled, err := Continue(draft, "at 9am", at.Add(time.Minute), "Europe/Brussels")
	if err != nil || filled.Missing != "" || filled.Title != "review the PR" || filled.Reminder.DueAt.Format(time.RFC3339) != "2026-11-13T08:00:00Z" {
		t.Fatal(filled, err)
	}
	draft, err = Parse("remind me in 10 minutes", at, "UTC")
	if err != nil || draft.Missing != "title" {
		t.Fatal(draft, err)
	}
	filled, err = Continue(draft, "Check backups", at.Add(2*time.Minute), "UTC")
	if err != nil || filled.Missing != "" || filled.Reminder.DueAt.Format(time.RFC3339) != "2026-11-12T12:10:00Z" {
		t.Fatal("relative due shifted during clarification", filled, err)
	}
}

func TestNaturalScheduleNormalizationUsesAuthorizedMorningDefault(t *testing.T) {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	for _, input := range []string{"remind me Friday at nine am to review logs", "remind me in ten minutes to check backups"} {
		draft, err := Parse(input, at, "UTC")
		if err != nil || draft.Missing != "" || draft.Reminder == nil {
			t.Fatal(input, draft, err)
		}
	}
	draft, err := Parse("remind me Friday morning to review logs", at, "UTC")
	if err != nil || draft.Missing != "" || draft.Reminder == nil || draft.Reminder.DueAt.Format(time.RFC3339) != "2026-11-13T09:00:00Z" {
		t.Fatal("authorized morning default was lost", draft, err)
	}
}

func TestReminderUnknownTimezoneIsClarifiedWithoutUTCDefault(t *testing.T) {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	draft, err := Parse("remind me tomorrow at 9am to review the PR", at, "")
	if err != nil || draft.Missing != "timezone" || draft.Reminder != nil || draft.Title != "review the PR" {
		t.Fatal(draft, err)
	}
	draft, err = Continue(draft, "Europe/Brussels", at, "")
	if err != nil || draft.Missing != "" || draft.Reminder.Timezone != "Europe/Brussels" {
		t.Fatal(draft, err)
	}
}

func TestReminderTitleClarificationDoesNotUseClockReplyAsTitle(t *testing.T) {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	draft, err := Parse("remind me in 10 minutes", at, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	filled, err := Continue(draft, "at 9am", at, "UTC")
	if err != nil || filled.Missing != "title" || filled.Title != "" {
		t.Fatal("clock reply became reminder title", filled, err)
	}
}

func TestWeekdayAndDaypartDefaultsPermitClockRefinementWithoutLosingDate(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		spec, want string
	}{
		{"on monday", "2026-11-16T08:00:00Z"},
		{"Monday", "2026-11-16T08:00:00Z"},
		{"on Monday morning", "2026-11-16T08:00:00Z"},
		{"tomorrow morning", "2026-11-13T08:00:00Z"},
		{"tomorrow at 25", "2026-11-13T08:00:00Z"},
	} {
		t.Run(test.spec, func(t *testing.T) {
			draft, err := Parse("remind me "+test.spec+" to go to the drycleaners", now, "Europe/Brussels")
			missing := ""
			if test.spec == "tomorrow at 25" {
				missing = "time"
			}
			if err != nil || draft.Missing != missing || (draft.Reminder == nil) != (missing != "") {
				t.Fatalf("default/explicit ambiguous clock policy was lost: %+v, %v", draft, err)
			}
			filled, err := Continue(draft, "9am", now.Add(time.Minute), "Europe/Brussels")
			if err != nil || filled.Missing != "" || filled.Reminder == nil || filled.Reminder.DueAt.Format(time.RFC3339) != test.want || filled.Title != "go to the drycleaners" {
				t.Fatalf("time reply lost the given date: %+v, %v", filled, err)
			}
		})
	}
}

func TestTimeClarificationPreservesExplicitTimezoneAndDate(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct{ spec, want string }{
		{"on Monday Europe/Brussels", "2026-11-16T08:00:00Z"},
		{"on Monday morning Europe/Brussels", "2026-11-16T08:00:00Z"},
		{"tomorrow at 25 Europe/Brussels", "2026-11-13T08:00:00Z"},
		{"on Monday UTC", "2026-11-16T09:00:00Z"},
	} {
		t.Run(test.spec, func(t *testing.T) {
			draft, err := Parse("remind me "+test.spec+" to collect my suit", now, "America/New_York")
			missing := ""
			if test.spec == "tomorrow at 25 Europe/Brussels" {
				missing = "time"
			}
			if err != nil || draft.Missing != missing || (draft.Reminder == nil) != (missing != "") {
				t.Fatalf("default/explicit ambiguous clock policy was lost: %+v, %v", draft, err)
			}
			filled, err := Continue(draft, "9am", now.Add(time.Minute), "America/New_York")
			if err != nil || filled.Missing != "" || filled.Reminder == nil || filled.Reminder.DueAt.Format(time.RFC3339) != test.want || filled.Reminder.Timezone != draft.Timezone || filled.Title != "collect my suit" {
				t.Fatalf("time reply lost the explicit date or timezone: %+v, %v", filled, err)
			}
		})
	}
}
