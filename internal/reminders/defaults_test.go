package reminders

import (
	"testing"
	"time"
)

func TestRelativeUnitAliasesUseTheOriginalExactAnchor(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 34, 56, 123, time.UTC)
	for _, test := range []struct {
		unit string
		step time.Duration
	}{
		{"s", time.Second}, {"sec", time.Second}, {"secs", time.Second}, {"second", time.Second}, {"seconds", time.Second},
		{"m", time.Minute}, {"min", time.Minute}, {"mins", time.Minute}, {"minute", time.Minute}, {"minutes", time.Minute},
		{"h", time.Hour}, {"hr", time.Hour}, {"hrs", time.Hour}, {"hour", time.Hour}, {"hours", time.Hour},
	} {
		for _, spacing := range []string{"", " "} {
			t.Run(test.unit+spacing, func(t *testing.T) {
				draft, err := Parse("remind me in 5"+spacing+test.unit+" to pickup Jane", now, "Europe/Brussels")
				if err != nil || draft.Missing != "" || draft.Reminder == nil || !draft.Reminder.DueAt.Equal(now.Add(5*test.step)) || draft.Title != "pickup Jane" {
					t.Fatalf("relative delay changed or asked for a clock: %+v, %v", draft, err)
				}
			})
		}
	}
	for _, input := range []string{"remind me to pickup Jane in 5m", "reminder: in 5M to pickup Jane"} {
		draft, err := Parse(input, now, "Europe/Brussels")
		if err != nil || draft.Missing != "" || draft.Title != "pickup Jane" || draft.Reminder == nil || !draft.Reminder.DueAt.Equal(now.Add(5*time.Minute)) {
			t.Errorf("relative suffix lost timing or title: %+v, %v", draft, err)
		}
	}
}

func TestRelativeDaysKeepLocalCivilTimeAcrossDST(t *testing.T) {
	for _, test := range []struct{ at, spec, want string }{
		{"2027-03-27T12:34:56Z", "in 1d", "2027-03-28T11:34:56Z"},
		{"2027-03-27T12:34:56Z", "in 1 day", "2027-03-28T11:34:56Z"},
		{"2027-10-30T11:34:56Z", "in 1days", "2027-10-31T12:34:56Z"},
		{"2027-03-27T12:34:56Z", "in 24h", "2027-03-28T12:34:56Z"},
		{"2026-11-13T23:58:30Z", "in 5m", "2026-11-14T00:03:30Z"},
	} {
		now, _ := time.Parse(time.RFC3339, test.at)
		draft, err := Parse("remind me "+test.spec+" to pickup Jane", now, "Europe/Brussels")
		if err != nil || draft.Missing != "" || draft.Reminder == nil || draft.Reminder.DueAt.Format(time.RFC3339) != test.want {
			t.Errorf("civil day and elapsed time were confused: %s => %+v, %v", test.spec, draft, err)
		}
	}
}

func TestAuthorizedCalendarDefaultsUseLocalDateAndExplicitClocksWin(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 30, 0, 0, time.UTC)
	for _, test := range []struct{ spec, want string }{
		{"tomorrow", "2026-11-14T08:00:00Z"},
		{"Monday", "2026-11-16T08:00:00Z"},
		{"on November 20", "2026-11-20T08:00:00Z"},
		{"tomorrow morning", "2026-11-14T08:00:00Z"},
		{"tomorrow afternoon", "2026-11-14T13:00:00Z"},
		{"tomorrow evening", "2026-11-14T17:00:00Z"},
		{"tonight", "2026-11-13T19:00:00Z"},
		{"Monday morning at 1:30pm", "2026-11-16T12:30:00Z"},
		{"Monday morning at 13:30", "2026-11-16T12:30:00Z"},
		{"tomorrow afternoon at noon", "2026-11-14T11:00:00Z"},
		{"tomorrow evening at midnight", "2026-11-13T23:00:00Z"},
		{"tonight at 9pm", "2026-11-13T20:00:00Z"},
		{"tomorrow at 12am", "2026-11-13T23:00:00Z"},
		{"tomorrow at 00:00", "2026-11-13T23:00:00Z"},
		{"tomorrow at 12pm", "2026-11-14T11:00:00Z"},
		{"tomorrow at 12:00", "2026-11-14T11:00:00Z"},
	} {
		t.Run(test.spec, func(t *testing.T) {
			draft, err := Parse("remind me "+test.spec+" to get eggs", now, "Europe/Brussels")
			if err != nil || draft.Missing != "" || draft.Reminder == nil || draft.Reminder.DueAt.Format(time.RFC3339) != test.want {
				t.Fatalf("authorized default or explicit override failed: %+v, %v", draft, err)
			}
		})
	}
}

func TestPassedSameDayDefaultStaysOnTheNamedDay(t *testing.T) {
	for _, test := range []struct{ at, spec, want string }{
		{"2026-11-13T12:30:45Z", "today", "2026-11-13T13:00:00Z"},
		{"2026-11-13T12:00:00Z", "today morning", "2026-11-13T13:00:00Z"},
		{"2026-11-13T22:20:00Z", "today", "2026-11-13T22:25:00Z"},
		{"2026-11-13T20:30:00Z", "tonight", "2026-11-13T21:00:00Z"},
		{"2026-11-13T12:30:00Z", "Friday", "2026-11-13T13:00:00Z"},
		{"2026-11-13T12:30:00Z", "on Friday", "2026-11-13T13:00:00Z"},
		{"2026-11-13T12:30:00Z", "next Friday", "2026-11-20T08:00:00Z"},
	} {
		now, _ := time.Parse(time.RFC3339, test.at)
		draft, err := Parse("remind me "+test.spec+" to get eggs", now, "Europe/Brussels")
		if err != nil || draft.Missing != "" || draft.Reminder == nil || draft.Reminder.DueAt.Format(time.RFC3339) != test.want {
			t.Errorf("same-day fallback changed day or picked a past time: %s => %+v, %v", test.spec, draft, err)
		}
	}
	late := time.Date(2026, 11, 13, 22, 57, 0, 0, time.UTC)
	draft, err := Parse("remind me today to get eggs", late, "Europe/Brussels")
	if err != nil || draft.Missing != "future_time" || draft.Reminder != nil {
		t.Fatalf("late default silently moved to tomorrow: %+v, %v", draft, err)
	}
}

func TestCalendarDefaultsFollowCivilTimeAcrossDSTChanges(t *testing.T) {
	for _, test := range []struct{ at, zone, want string }{
		{"2027-03-27T12:30:00Z", "Europe/Brussels", "2027-03-28T07:00:00Z"},
		{"2027-10-30T12:30:00Z", "Europe/Brussels", "2027-10-31T08:00:00Z"},
		{"2027-03-13T12:30:00Z", "America/New_York", "2027-03-14T13:00:00Z"},
	} {
		now, _ := time.Parse(time.RFC3339, test.at)
		draft, err := Parse("remind me tomorrow to get eggs", now, test.zone)
		if err != nil || draft.Missing != "" || draft.Reminder == nil || draft.Reminder.DueAt.Format(time.RFC3339) != test.want {
			t.Errorf("default09:00 changed its civil hour at DST: %+v, %v", draft, err)
		}
	}
}

func TestDefaultsDoNotRepairInvalidOrConflictingExplicitTiming(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 30, 0, 0, time.UTC)
	for _, spec := range []string{
		"in 0m", "in -5m", "in 1.5m", "in 999999999999h", "in 999999d", "in 5ms",
		"in 5m at 9am", "in 5m tomorrow", "tomorrow in 5m", "in 5m or 10m",
		"today at 9am", "tomorrow at 25", "tomorrow at 24:00", "tomorrow at 13pm",
		"this Monday", "on 2025-11-20", "tomorrow morning afternoon", "next week", "on 03/04",
	} {
		draft, err := Parse("remind me "+spec+" to get eggs", now, "Europe/Brussels")
		if err != nil || draft.Missing == "" || draft.Reminder != nil {
			t.Errorf("unsupported/conflicting timing was silently repaired: %q => %+v, %v", spec, draft, err)
		}
	}
}

func TestDefaultDateAndRelativeAnchorSurviveClarification(t *testing.T) {
	now := time.Date(2026, 11, 13, 22, 58, 0, 0, time.UTC)
	for _, test := range []struct{ spec, want string }{
		{"tomorrow", "2026-11-14T08:00:00Z"},
		{"in 5m", "2026-11-13T23:03:00Z"},
	} {
		draft, err := Parse("remind me "+test.spec, now, "Europe/Brussels")
		if err != nil || draft.Missing != "title" || draft.Reminder == nil {
			t.Fatalf("missing title lost resolved timing: %+v, %v", draft, err)
		}
		filled, err := Continue(draft, "pickup Jane", now.Add(2*time.Minute), "America/New_York")
		if err != nil || filled.Missing != "" || filled.Reminder == nil || filled.Reminder.DueAt.Format(time.RFC3339) != test.want || filled.Timezone != "Europe/Brussels" {
			t.Errorf("clarification shifted the anchor or timezone: %+v, %v", filled, err)
		}
	}
}

func TestClockRefinementPreservesTheOriginalDefaultDate(t *testing.T) {
	now := time.Date(2026, 11, 13, 22, 58, 0, 0, time.UTC)
	draft, err := Parse("remind me Monday afternoon to pickup Jane", now, "Europe/Brussels")
	if err != nil || draft.Missing != "" {
		t.Fatal(draft, err)
	}
	filled, err := Continue(draft, "1:30pm", now.Add(3*time.Minute), "America/New_York")
	if err != nil || filled.Missing != "" || filled.Reminder == nil || filled.Reminder.DueAt.Format(time.RFC3339) != "2026-11-16T12:30:00Z" {
		t.Fatalf("clock refinement lost the original day/timezone after midnight: %+v, %v", filled, err)
	}
}
