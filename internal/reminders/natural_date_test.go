package reminders

import (
	"testing"
	"time"
)

func TestNaturalCalendarAndClockVariantsRemainGrounded(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC) // Friday, 13:00 Brussels.
	for _, test := range []struct{ spec, want string }{
		{"Mon. at 9 a.m.", "2026-11-16T08:00:00Z"},
		{"on Tues at 18:30", "2026-11-17T17:30:00Z"},
		{"this Friday at 6pm", "2026-11-13T17:00:00Z"},
		{"this Sunday at noon", "2026-11-15T11:00:00Z"},
		{"tomorrow at midnight", "2026-11-13T23:00:00Z"},
		{"day after tomorrow at 9am", "2026-11-15T08:00:00Z"},
		{"tomorrow 9am", "2026-11-14T08:00:00Z"},
		{"on November 20 at 9am", "2026-11-20T08:00:00Z"},
		{"on 20 Nov at 9am", "2026-11-20T08:00:00Z"},
		{"on November 20th, 2026 at 9am", "2026-11-20T08:00:00Z"},
		{"on Jan 2 at 9am", "2027-01-02T08:00:00Z"},
		{"for this Sun at noon", "2026-11-15T11:00:00Z"},
		{"on next Mon at 9am", "2026-11-16T08:00:00Z"},
		{"tomorrow 9am utc", "2026-11-14T09:00:00Z"},
	} {
		t.Run(test.spec, func(t *testing.T) {
			draft, err := Parse("remind me "+test.spec+" to get eggs", now, "Europe/Brussels")
			if err != nil || draft.Missing != "" || draft.Title != "get eggs" || draft.Reminder == nil || draft.Reminder.DueAt.Format(time.RFC3339) != test.want {
				t.Fatalf("natural explicit schedule was lost or changed: %+v, %v", draft, err)
			}
		})
	}
}

func TestNaturalDatesUseAuthorizedDefaultAndPermitExplicitClockRefinement(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct{ spec, want string }{
		{"this Sunday", "2026-11-15T08:00:00Z"},
		{"Mon.", "2026-11-16T08:00:00Z"},
		{"November 20", "2026-11-20T08:00:00Z"},
		{"day after tomorrow", "2026-11-15T08:00:00Z"},
	} {
		t.Run(test.spec, func(t *testing.T) {
			draft, err := Parse("remind me "+test.spec+" to get eggs", now, "Europe/Brussels")
			if err != nil || draft.Missing != "" || draft.Reminder == nil || draft.Reminder.DueAt.Format(time.RFC3339) != test.want {
				t.Fatalf("date-only input lost its authorized default: %+v, %v", draft, err)
			}
			filled, err := Continue(draft, "9am", now.Add(time.Minute), "America/New_York")
			if err != nil || filled.Missing != "" || filled.Reminder == nil || filled.Reminder.DueAt.Format(time.RFC3339) != test.want {
				t.Fatalf("clarification lost the original date or timezone: %+v, %v", filled, err)
			}
		})
	}
}

func TestAmbiguousOrPastCalendarWordingDoesNotInventAFutureDate(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	for _, spec := range []string{"this Monday at 9am", "on November 20 2025 at 9am", "on 03/04 at 9am", "next week at 9am", "this weekend at 9am", "on February 30 at 9am"} {
		draft, err := Parse("remind me "+spec+" to get eggs", now, "Europe/Brussels")
		if err != nil || draft.Missing == "" || draft.Reminder != nil {
			t.Errorf("ambiguous or past calendar was silently shifted: %q => %+v, %v", spec, draft, err)
		}
	}
}

func TestExplicitReminderRequestsMayPutNaturalDatesAfterTheTitle(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct{ input, due, missing string }{
		{"remind me to get eggs Monday", "2026-11-16T08:00:00Z", ""},
		{"remind me to get eggs Mon.", "2026-11-16T08:00:00Z", ""},
		{"remind me to get eggs this Sun at noon", "2026-11-15T11:00:00Z", ""},
		{"remind me to get eggs Nov 20 9am", "2026-11-20T08:00:00Z", ""},
		{"remind me to get eggs day after tomorrow at 9am", "2026-11-15T08:00:00Z", ""},
	} {
		draft, err := Parse(test.input, now, "Europe/Brussels")
		if err != nil || draft.Title != "get eggs" || draft.Missing != test.missing || test.due != "" && (draft.Reminder == nil || draft.Reminder.DueAt.Format(time.RFC3339) != test.due) || test.due == "" && draft.Reminder != nil {
			t.Errorf("suffix scheduling became task content or lost timing: %q => %+v, %v", test.input, draft, err)
		}
	}
}
