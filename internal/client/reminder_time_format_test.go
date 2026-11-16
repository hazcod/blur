package client

import (
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/tasks"
)

func TestReminderReceiptsUseLocalizedDisplayAndKeepEditInputParseable(t *testing.T) {
	at := time.Date(2026, 11, 13, 12, 30, 0, 0, time.UTC)
	r := tasks.Reminder{DueAt: at, Timezone: "Europe/Brussels"}
	if got, long := reminderDateTimeCopy(r), darwin.LocalizedDateTime(at, r.Timezone); got == long {
		t.Fatalf("scheduled reminder still uses the long date notation: %q", got)
	}
	if got, want := reminderScheduleCopy(r), darwin.LocalizedShortDateTime(at, r.Timezone); got != want {
		t.Fatalf("one-time reminder ignored system region/clock preference: %q want %q", got, want)
	}
	if strings.Contains(reminderScheduleCopy(r), r.Timezone) {
		t.Fatal("short schedule label exposed the timezone", reminderScheduleCopy(r))
	}
	for _, repeat := range []tasks.Recurrence{tasks.Daily, tasks.Weekly} {
		r.Repeat, r.Hour, r.Minute, r.Weekday = repeat, 13, 30, time.Friday
		if got := reminderScheduleCopy(r); !strings.Contains(got, darwin.LocalizedTime(at, r.Timezone)) || strings.Contains(got, r.Timezone) {
			t.Errorf("recurring reminder was not short and localized: %q", got)
		}
	}
	r.Repeat = tasks.Once
	if got := taskWhenText(tasks.Item{Reminder: &r}); got != "on 2026-11-13 at 13:30" {
		t.Fatalf("localized display made editor syntax ambiguous: %q", got)
	}
}

func TestLocalizedReceiptEvidenceStillBindsTheExactQueuedInstant(t *testing.T) {
	at := time.Date(2026, 11, 13, 12, 30, 0, 0, time.UTC)
	item := tasks.Item{ID: "11111111111111111111111111111111", Revision: 1, Title: "pickup Jane", State: tasks.Open, CreatedAt: at, UpdatedAt: at,
		Reminder: &tasks.Reminder{DueAt: at.Add(time.Hour), Timezone: "Europe/Brussels"},
		Delivery: tasks.Delivery{State: tasks.Scheduled, At: at, Attempted: true, NextAt: at.Add(time.Hour)},
	}
	before, _ := taskEvidence(item, at, "")
	item.Delivery.NextAt = item.Delivery.NextAt.Add(time.Nanosecond)
	after, _ := taskEvidence(item, at, "")
	if before.SourceID == after.SourceID || !strings.Contains(after.Text, item.Delivery.NextAt.UTC().Format(time.RFC3339Nano)) {
		t.Fatalf("localized clock discarded exact queue provenance: before=%s after=%s text=%q", before.SourceID, after.SourceID, after.Text)
	}
}
