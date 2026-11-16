package client

import (
	"testing"
	"time"

	"github.com/hazcod/blur/internal/tasks"
)

func TestLocalTaskReadFiltersUseStatusHistoryAndCivilCalendar(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, zone)
	for _, test := range []struct {
		input                                     string
		state, history                            tasks.State
		start, end                                string
		reminders, count, next, overdue, upcoming bool
	}{
		{input: "all tasks?"},
		{input: "pending todos?", state: tasks.Open},
		{input: "show my tasks not done yet?", state: tasks.Open},
		{input: "How many completed todos yesterday?", history: tasks.Completed, start: "2026-11-12T00:00:00+01:00", end: "2026-11-13T00:00:00+01:00", count: true},
		{input: "cancelled tasks last week?", history: tasks.Cancelled, start: "2026-11-02T00:00:00+01:00", end: "2026-11-09T00:00:00+01:00"},
		{input: "which tasks did I cancel yesterday?", history: tasks.Cancelled, start: "2026-11-12T00:00:00+01:00", end: "2026-11-13T00:00:00+01:00"},
		{input: "which todos did I finish yesterday?", history: tasks.Completed, start: "2026-11-12T00:00:00+01:00", end: "2026-11-13T00:00:00+01:00"},
		{input: "show reminders tomorrow?", state: tasks.Open, reminders: true, start: "2026-11-14T00:00:00+01:00", end: "2026-11-15T00:00:00+01:00"},
		{input: "my next reminder?", state: tasks.Open, reminders: true, next: true},
		{input: "reminders next week?", state: tasks.Open, reminders: true, start: "2026-11-16T00:00:00+01:00", end: "2026-11-23T00:00:00+01:00"},
		{input: "show my overdue tasks?", state: tasks.Open, reminders: true, overdue: true},
		{input: "show my upcoming reminders?", state: tasks.Open, reminders: true, upcoming: true},
	} {
		t.Run(test.input, func(t *testing.T) {
			query, ok := parseTaskQuery(test.input, now)
			if !ok || query.State != test.state || query.HistoryState != test.history || query.RemindersOnly != test.reminders || query.Count != test.count || query.Next != test.next || query.Overdue != test.overdue || query.Upcoming != test.upcoming {
				t.Fatalf("task read lost grounded filters: %+v matched=%t", query, ok)
			}
			if test.start != "" && (query.Start.Format(time.RFC3339) != test.start || query.End.Format(time.RFC3339) != test.end) {
				t.Fatalf("calendar period used wrong timezone/bounds: %+v", query)
			}
		})
	}
	for _, input := range []string{"open completed tasks?", "cancelled pending todos?", "tasks today tomorrow?"} {
		if query, ok := parseTaskQuery(input, now); ok {
			t.Fatalf("conflicting filter silently selected one result: %q %+v", input, query)
		}
	}
}

func TestOverdueAndUpcomingTaskReadsExcludeUndatedAndWrongSideOfNow(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	for _, overdue := range []bool{true, false} {
		query := taskQuery{State: tasks.Open, RemindersOnly: true, Overdue: overdue, Upcoming: !overdue}
		for _, offset := range []time.Duration{-time.Hour, 0, time.Hour} {
			item := tasks.Item{State: tasks.Open, Reminder: &tasks.Reminder{DueAt: now.Add(offset), Timezone: "UTC"}}
			_, matched, _ := matchTaskQuery(item, query, now)
			want := overdue && offset < 0 || !overdue && offset > 0
			if matched != want {
				t.Fatalf("wrong due-side filter: overdue=%t offset=%s matched=%t", overdue, offset, matched)
			}
		}
		if _, matched, _ := matchTaskQuery(tasks.Item{State: tasks.Open}, query, now); matched {
			t.Fatal("undated task acquired fabricated due time")
		}
	}
}
