package reminders

import (
	"testing"
	"time"

	"github.com/hazcod/blur/internal/tasks"
)

func TestExplicitReminderParsingKeepsTitleAndExactSchedule(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) // 13:00 Brussels
	for _, test := range []struct {
		input, title, want string
		repeat             tasks.Recurrence
	}{
		{"remind me in 10 minutes to review the PR", "review the PR", "2026-11-12T12:10:00Z", tasks.Once},
		{"remind me about the compiler release in 10 minutes", "the compiler release", "2026-11-12T12:10:00Z", tasks.Once},
		{"remind me to review the PR tomorrow at 09:30", "review the PR", "2026-11-13T08:30:00Z", tasks.Once},
		{"reminder: tomorrow at 9am to rotate the certificate", "rotate the certificate", "2026-11-13T08:00:00Z", tasks.Once},
		{"remind me on 2026-11-20 at 17:00 to check the build", "check the build", "2026-11-20T16:00:00Z", tasks.Once},
		{"remind me every day at 09:00 to check backups", "check backups", "2026-11-13T08:00:00Z", tasks.Daily},
		{"remind me every friday at 16:00 to write the weekly review", "write the weekly review", "2026-11-13T15:00:00Z", tasks.Weekly},
		{"remind me next monday at 18:00 to review logs", "review logs", "2026-11-16T17:00:00Z", tasks.Once},
		{"remind me on 2027-10-31 at 02:30 first to check the server", "check the server", "2027-10-31T00:30:00Z", tasks.Once},
		{"remind me on 2027-10-31 at 02:30 second to check the server", "check the server", "2027-10-31T01:30:00Z", tasks.Once},
		{"remind me at 2027-10-31T02:30:00+01:00 to check the server", "check the server", "2027-10-31T01:30:00Z", tasks.Once},
	} {
		t.Run(test.input, func(t *testing.T) {
			draft, err := Parse(test.input, now, "Europe/Brussels")
			if err != nil || draft.Missing != "" || draft.Title != test.title || draft.Reminder == nil || draft.Reminder.DueAt.Format(time.RFC3339) != test.want || draft.Reminder.Repeat != test.repeat {
				t.Fatalf("%+v %v", draft, err)
			}
		})
	}
}

func TestMissingOrAmbiguousReminderFieldsAreNotInvented(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	for _, input := range []string{"remind me to review the PR", "remind me at 25 to review the PR", "remind me in 10 minutes", "remind me on 2020-01-01 at 09:00 to review the PR"} {
		draft, err := Parse(input, now, "Europe/Brussels")
		if err != nil || draft.Missing == "" {
			t.Fatalf("missing field silently guessed for %q: %+v %v", input, draft, err)
		}
	}
	draft, err := Parse("remind me on 2027-03-28 at 02:30 to check the server", now, "Europe/Brussels")
	if err != nil || draft.Missing != "nonexistent_time" {
		t.Fatalf("DST gap not explained: %+v %v", draft, err)
	}
	draft, err = Parse("remind me on 2027-10-31 at 02:30 to check the server", now, "Europe/Brussels")
	if err != nil || draft.Missing != "ambiguous_time" {
		t.Fatalf("DST fold not explained: %+v %v", draft, err)
	}
}
