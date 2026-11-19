package reminders

import (
	"testing"
	"time"
)

func TestBareHourWithoutMeridiemResolvesToLikelyDaypart(t *testing.T) {
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.UTC)
	for _, test := range []struct{ when, want string }{
		{"tomorrow at 9", "2026-11-17T08:00:00Z"},
		{"tomorrow at 7", "2026-11-17T06:00:00Z"},
		{"tomorrow at 12", "2026-11-17T11:00:00Z"},
		{"tomorrow at 5", "2026-11-17T16:00:00Z"},
		{"tomorrow at 1", "2026-11-17T12:00:00Z"},
		{"tomorrow at 6", "2026-11-17T17:00:00Z"},
		{"tomorrow at 5am", "2026-11-17T04:00:00Z"},
		{"tomorrow at 9pm", "2026-11-17T20:00:00Z"},
		{"tomorrow at 17", "2026-11-17T16:00:00Z"},
		{"tomorrow at 9:30", "2026-11-17T08:30:00Z"},
	} {
		draft, err := Parse("remind me "+test.when+" to send the report", now, "Europe/Brussels")
		if err != nil || draft.Missing != "" || draft.Reminder == nil || draft.Reminder.DueAt.Format(time.RFC3339) != test.want {
			t.Errorf("%q: %+v %v", test.when, draft, err)
		}
	}
}
