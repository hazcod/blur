package client

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-gui/goui/gui"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/tasks"
	"github.com/hazcod/blur/internal/today"
)

func mountedTodayText(t *testing.T, d *desktop) string {
	t.Helper()
	t.Cleanup(func() { hourSoFarStates.Delete(d) })
	var labels []string
	var walk func(gui.Widget)
	walk = func(widget gui.Widget) {
		if label, ok := widget.(*gui.Label); ok {
			labels = append(labels, label.Text())
		}
		for _, child := range widget.Children() {
			walk(child)
		}
	}
	walk(mountVoicePopupView(t, d.todayView()))
	return strings.Join(labels, "\n")
}

func TestTodayMountedViewShowsOnlyRecapsAndTasks(t *testing.T) {
	c, _, at := todayTaskClient(t)
	d := &desktop{
		client: c, date: "2026-11-12", note: "A note draft", quickNoteExpanded: true,
		today: TodayView{
			State: "ready",
			Snapshot: today.Snapshot{
				ActiveSubjects: []today.Subject{{Label: "Private app subject"}},
				TimeSplit:      []today.TimeSplit{{Label: "Private time split", Duration: time.Hour}},
				RecentChanges:  []today.Change{{Text: "Private recent activity"}},
				OpenLoops:      []today.OpenLoop{{Text: "Private open loop"}},
				Questions:      []today.Question{{ID: "question", Text: "Private follow-up question"}},
				TodoCandidates: []today.Todo{{ID: "candidate", Text: "Send the release plan"}},
			},
			RecentCaptures:  []RecentCapture{{SearchHit: SearchHit{AppID: "com.example.editor", Text: "Private raw capture"}, CapturedAt: at}},
			Tasks:           []tasks.Item{{ID: "11111111111111111111111111111111", Title: "Review the release plan", State: tasks.Open}},
			TaskIssues:      1,
			UnimportedTasks: []today.Todo{{ID: "legacy", Text: "Recover the saved task"}},
		},
		hourlySummaries: []chronicle.Summary{{HourID: "hour:20261112T10Z", Start: at.Add(-time.Hour), End: at, Text: "Reviewed the rollout with Mira."}},
	}
	text := mountedTodayText(t, d)
	for _, required := range []string{"Hourly recaps", "Reviewed the rollout with Mira.", "Saved tasks", "Review the release plan", "Suggested tasks", "Send the release plan", "Recover the saved task", "Add task", "Edit", "Complete", "Cancel task"} {
		if !strings.Contains(text, required) {
			t.Errorf("mounted Today omits %q:\n%s", required, text)
		}
	}
	for _, removed := range []string{"Time in apps", "Recent activity", "Recent captured content", "Open loops", "Quick note", "A note draft", "Private app subject", "Private time split", "Private recent activity", "Private open loop", "Private follow-up question", "Private raw capture", "No recaps or tasks"} {
		if strings.Contains(text, removed) {
			t.Errorf("mounted Today still shows %q:\n%s", removed, text)
		}
	}
}

func TestTodayMountedEmptyStateUsesOnlyVisibleRecapsAndTasks(t *testing.T) {
	c, _, at := todayTaskClient(t)
	for _, test := range []struct {
		name      string
		view      TodayView
		recaps    []chronicle.Summary
		loading   bool
		recapLoad bool
		recapErr  bool
		wantEmpty bool
		wantText  string
	}{
		{name: "no snapshot", view: TodayView{State: "empty"}, wantEmpty: true},
		{name: "hidden activity", view: TodayView{State: "ready", Snapshot: today.Snapshot{
			ActiveSubjects: []today.Subject{{Label: "Slack"}},
			TimeSplit:      []today.TimeSplit{{Label: "Slack", Duration: time.Hour}},
			RecentChanges:  []today.Change{{Text: "Active in Slack"}},
			OpenLoops:      []today.OpenLoop{{Text: "Awaiting a reply"}},
			Questions:      []today.Question{{ID: "question", Text: "Who owns this?"}},
		}, RecentCaptures: []RecentCapture{{SearchHit: SearchHit{AppID: "com.example.editor", Text: "Raw captured content"}}}}, wantEmpty: true},
		{name: "saved task", view: TodayView{State: "ready", Tasks: []tasks.Item{{Title: "Review Atlas", State: tasks.Open}}}, wantText: "Review Atlas"},
		{name: "suggested task", view: TodayView{State: "ready", Snapshot: today.Snapshot{TodoCandidates: []today.Todo{{ID: "candidate", Text: "Send Atlas"}}}}, wantText: "Send Atlas"},
		{name: "task needs review", view: TodayView{State: "ready", TaskIssues: 1, UnimportedTasks: []today.Todo{{ID: "legacy", Text: "Recover Atlas"}}}, wantText: "Recover Atlas"},
		{name: "recap without snapshot", view: TodayView{State: "empty"}, recaps: []chronicle.Summary{{Start: at.Add(-time.Hour), End: at, Text: "Reviewed Atlas."}}, wantText: "Reviewed Atlas."},
		{name: "loading day", loading: true, wantText: "Loading recaps and tasks…"},
		{name: "loading recaps", view: TodayView{State: "ready"}, recapLoad: true, wantText: "Checking completed hours…"},
		{name: "recap error", view: TodayView{State: "ready"}, recapErr: true, wantText: "Hourly recaps could not be read. Try again."},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := &desktop{client: c, date: "2026-11-12", today: test.view, todayLoading: test.loading, hourlySummaries: test.recaps, hourlyLoading: test.recapLoad, hourlyError: test.recapErr}
			text := mountedTodayText(t, d)
			if got := strings.Contains(text, "No recaps or tasks for this day yet."); got != test.wantEmpty {
				t.Fatalf("empty state = %t, want %t:\n%s", got, test.wantEmpty, text)
			}
			if test.wantText != "" && !strings.Contains(text, test.wantText) {
				t.Fatalf("mounted Today omits %q:\n%s", test.wantText, text)
			}
			for _, staleEmpty := range []string{"No open items", "No recap for "} {
				if strings.Contains(text, staleEmpty) {
					t.Fatalf("mounted Today shows obsolete empty state %q:\n%s", staleEmpty, text)
				}
			}
		})
	}
}
