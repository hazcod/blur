package client

import (
	"context"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/intent"
)

func TestNaturalCompactRelativeReminderResolvesWithoutAskingForATime(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	now := time.Date(2026, 11, 13, 12, 34, 56, 123, time.UTC)
	c.askNow = func() time.Time { return now }
	for _, input := range []string{
		"in 5m i need to pickup Jane", "I need to pickup Jane in 5 mins", "in 5min: pickup Jane", "remind me in 5m to pickup Jane", "remind me to pickup Jane in 5m",
		"in5m i need to pickup Jane", "in 5 m I need to pickup Jane", "in 5 MIN I need to pickup Jane", "in  5  minutes  I need to pickup Jane", "remind me 5min to pickup Jane",
	} {
		resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "Europe/Brussels")
		if err != nil || resolved.Action.Kind != intent.Reminder || resolved.Draft == nil || resolved.Draft.Title != "pickup Jane" || resolved.Draft.Missing != "" || resolved.Draft.Reminder == nil || !resolved.Draft.Reminder.DueAt.Equal(now.Add(5*time.Minute)) {
			t.Errorf("compact relative request did not schedule exactly now+5m: %q => %+v, %v", input, resolved, err)
		}
	}
}

func TestRelativeDelayAloneAsksForATitleAndKeepsTheOriginalDueTime(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	now := time.Date(2026, 11, 13, 12, 34, 56, 0, time.UTC)
	c.askNow = func() time.Time { return now }
	for _, input := range []string{"5m", "5 m", "5 minutes", "5min", "5 min", "in5m", "IN 5 MINS"} {
		resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "Europe/Brussels")
		if err != nil || resolved.Action.Kind != intent.Reminder || resolved.Draft == nil || resolved.Draft.Missing != "title" || resolved.Draft.Reminder == nil || !resolved.Draft.Reminder.DueAt.Equal(now.Add(5*time.Minute)) {
			t.Errorf("time-only shorthand was saved as a note or lost its delay: %q => %+v, %v", input, resolved, err)
			continue
		}
		now = now.Add(time.Minute)
		filled, err := c.ResolveEntry(context.Background(), "pickup Jane", EntryState{Epoch: resolved.Epoch, Pending: resolved.Draft}, "America/New_York")
		if err != nil || filled.Draft == nil || filled.Draft.Missing != "" || filled.Draft.Title != "pickup Jane" || filled.Draft.Reminder == nil || !filled.Draft.Reminder.DueAt.Equal(now.Add(4*time.Minute)) {
			t.Errorf("title clarification moved relative delay: %+v, %v", filled, err)
		}
	}
}

func TestConflictingNaturalSchedulesClarifyAndQuantitiesGrantNoTiming(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	c.askNow = func() time.Time { return time.Date(2026, 11, 13, 12, 30, 0, 0, time.UTC) }
	for _, input := range []string{"in 5m I need to pickup Jane tomorrow", "I need to pickup Jane in 5m at 9am", "tomorrow I need to pickup Jane in 5m"} {
		resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "Europe/Brussels")
		if err != nil || resolved.Action.Kind != intent.Clarify || resolved.Draft != nil || resolved.Target != nil || resolved.Message == "" {
			t.Errorf("conflicting schedule wasn't clarified before execution: %q => %+v, %v", input, resolved, err)
		}
	}
	for _, input := range []string{"The meeting lasted 5 minutes", "I need to review 5 minutes of logs", "Note: remind me in 5m to pickup Jane", "`sleep 5m`", "Mira said \"in 5m pickup Jane\""} {
		resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "Europe/Brussels")
		if err != nil || resolved.Action.Kind == intent.Reminder || resolved.Draft != nil || resolved.Target != nil {
			t.Errorf("quantity/code/quoted note acquired reminder timing: %q => %+v, %v", input, resolved, err)
		}
	}
}

func TestNaturalDateAndDaypartDefaultsResolveInTheUsersTimezone(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	c.askNow = func() time.Time { return time.Date(2026, 11, 13, 23, 30, 0, 0, time.UTC) }
	for _, test := range []struct{ input, want string }{
		{"tomorrow get eggs", "2026-11-15T08:00:00Z"},
		{"Monday: get eggs", "2026-11-16T08:00:00Z"},
		{"I need to get eggs tomorrow afternoon", "2026-11-15T13:00:00Z"},
		{"I need to get eggs tomorrow morning at 13:30", "2026-11-15T12:30:00Z"},
		{"I need to get eggs tomorrow morning at 1:30pm", "2026-11-15T12:30:00Z"},
		{"this morning I need to get eggs", "2026-11-14T08:00:00Z"},
		{"this afternoon I need to get eggs", "2026-11-14T13:00:00Z"},
		{"this evening I need to get eggs", "2026-11-14T17:00:00Z"},
	} {
		resolved, err := c.ResolveEntry(context.Background(), test.input, EntryState{}, "Europe/Brussels")
		if err != nil || resolved.Action.Kind != intent.Reminder || resolved.Draft == nil || resolved.Draft.Missing != "" || resolved.Draft.Reminder == nil || resolved.Draft.Reminder.DueAt.Format(time.RFC3339) != test.want {
			t.Errorf("authorized default/clock lost local date: %q => %+v, %v", test.input, resolved, err)
		}
	}
}
