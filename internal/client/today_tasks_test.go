package client

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

func todayTaskClient(t *testing.T) (*Client, *durable.Store, time.Time) {
	t.Helper()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	c := NewClient(s).WithNotes(notes.NewService(s, nil, func() time.Time { return at }))
	c.askNow = func() time.Time { return at }
	service, err := reminders.NewService(s, &clientNoticePort{}, reminders.Options{Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	c.reminders = service
	t.Cleanup(func() { c.Close(); service.Close(); _ = s.Close() })
	return c, s, at
}

func TestTodayUsesCurrentTaskStateAndBackfillsOnlyConfirmedLegacyTodos(t *testing.T) {
	c, s, _ := todayTaskClient(t)
	if _, err := c.AddNote("TODO: Review Atlas\nTODO: Check backups"); err != nil {
		t.Fatal(err)
	}
	view, err := c.Today("2026-11-12")
	if err != nil || len(view.Snapshot.TodoCandidates) != 2 || len(view.Tasks) != 0 {
		t.Fatal("suggestions activated without confirmation", view, err)
	}
	first := view.Snapshot.TodoCandidates[0]
	if err := c.ConfirmProposal(view.Date, first.ID); err != nil {
		t.Fatal(err)
	}
	view, err = c.Today(view.Date)
	if err != nil || len(view.Tasks) != 1 || len(view.Snapshot.ConfirmedTodos) != 1 || view.Snapshot.ConfirmedTodos[0].TaskID != view.Tasks[0].ID {
		t.Fatal("Today did not load its real task", view, err)
	}
	item := view.Tasks[0]
	item.Title, item.State = "A corrected task title", tasks.Completed
	if _, err := c.UpdateTask(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	// A pre-upgrade confirmed item has no task receipt yet.
	stored, _, err := s.GetTodaySnapshot(context.Background(), view.Date)
	if err != nil {
		t.Fatal(err)
	}
	legacy := stored.TodoCandidates[0]
	legacy.Confirmed = true
	stored.TodoCandidates = nil
	stored.ConfirmedTodos = append(stored.ConfirmedTodos, legacy)
	if err := s.PutTodaySnapshot(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	view, err = c.Today(view.Date)
	if err != nil || view.TaskIssues != 0 || len(view.Tasks) != 2 || len(view.Snapshot.ConfirmedTodos) != 1 || view.Snapshot.ConfirmedTodos[0].ID != legacy.ID {
		t.Fatal("legacy confirmation did not backfill or completion remained active in Today", view, err)
	}
	var imported tasks.Item
	for _, task := range view.Tasks {
		if task.ID != item.ID {
			imported = task
		} else if task.Title != "A corrected task title" || task.State != tasks.Completed {
			t.Fatal("Today displayed stale task text/state", task)
		}
	}
	if !tasks.ValidID(imported.ID) || imported.Reminder != nil {
		t.Fatal("legacy task was not imported without scheduling", imported)
	}
	if err := c.DeleteTask(context.Background(), imported.ID, imported.Revision); err != nil {
		t.Fatal(err)
	}
	view, err = c.Today(view.Date)
	if err != nil || len(view.Tasks) != 1 || len(view.Snapshot.ConfirmedTodos) != 0 {
		t.Fatal("viewing Today restored a removed task", view, err)
	}
	page, err := c.TaskPage(context.Background(), "", "")
	if err != nil || len(page.Items) != 1 {
		t.Fatal("Today backfill created duplicates", page, err)
	}
}

func TestTodayConfirmationClickCannotWriteAfterMemoryInvalidation(t *testing.T) {
	c, s, _ := todayTaskClient(t)
	if _, err := c.AddNote("TODO: Review Atlas"); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := s.GetTodaySnapshot(context.Background(), "2026-11-12")
	if err != nil || len(snapshot.TodoCandidates) != 1 {
		t.Fatal(err)
	}
	app := &taskInterleaveApp{posted: make(chan func(), 1), update: func() { c.NotifyMemoryChanging(); c.NotifyMemoryStable() }}
	d := desktop{client: c, app: app, today: TodayView{Date: snapshot.Date, Snapshot: snapshot}}
	d.confirm(snapshot.TodoCandidates[0].ID)
	select {
	case <-app.posted:
	case <-time.After(5 * time.Second):
		t.Fatal("confirmation did not finish")
	}
	page, err := c.TaskPage(context.Background(), "", "")
	if err != nil || len(page.Items) != 0 {
		t.Fatal("a pre-deletion click activated a task in a new epoch", page, err)
	}
	current, _, err := s.GetTodaySnapshot(context.Background(), snapshot.Date)
	if err != nil || len(current.ConfirmedTodos) != 0 || len(current.TodoCandidates) != 1 {
		t.Fatal("stale confirmation changed Today", current, err)
	}
}

func TestTaskSourceInspectionReturnsOriginalEvidenceWithoutAModel(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	text := "Meeting notes\nTODO: Review Atlas\nThe design decision was to keep storage encrypted."
	added, err := c.AddNote(text)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ConfirmProposal("2026-11-12", added.Proposals[0].ID); err != nil {
		t.Fatal(err)
	}
	page, err := c.TaskPage(context.Background(), "", "")
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	item := page.Items[0]
	evidence, err := c.TaskSourceEvidence(context.Background(), item.ID, item.Revision)
	if err != nil || len(evidence) != 1 || evidence[0].SourceID != added.SourceID || evidence[0].Text != text || !strings.Contains(evidence[0].Text, "keep storage encrypted") {
		t.Fatal("task provenance could not be inspected", evidence, err)
	}
	item.Title = "Review the Atlas release"
	if _, err := c.UpdateTask(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if _, err := c.TaskSourceEvidence(context.Background(), item.ID, item.Revision); err != tasks.ErrConflict {
		t.Fatal("stale task inspection was not revision-bound", err)
	}
}

func TestCompletingTaskFromTodayRefreshesItsDisplayedState(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	added, err := c.AddNote("TODO: Review Atlas")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ConfirmProposal("2026-11-12", added.Proposals[0].ID); err != nil {
		t.Fatal(err)
	}
	view, err := c.Today("2026-11-12")
	if err != nil || len(view.Tasks) != 1 {
		t.Fatal(view, err)
	}
	app := &taskInterleaveApp{posted: make(chan func(), 8)}
	d := desktop{client: c, app: app, page: "Today", date: view.Date, today: view}
	d.changeTaskState(view.Tasks[0], tasks.Completed)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for len(d.today.Tasks) != 1 || d.today.Tasks[0].State != tasks.Completed || d.loading || d.todayLoading || d.hourlyLoading {
		select {
		case publish := <-app.posted:
			publish()
		case <-deadline.C:
			t.Fatal("task change or Today refresh did not publish")
		}
	}
	if len(d.today.Tasks) != 1 || d.today.Tasks[0].State != tasks.Completed || len(d.today.Snapshot.ConfirmedTodos) != 0 {
		t.Fatal("Today continued displaying the completed task as open", d.today)
	}
}

func TestTodayQuestionClickCannotWriteAfterMemoryInvalidation(t *testing.T) {
	for _, action := range []string{"answer", "dismiss"} {
		t.Run(action, func(t *testing.T) {
			c, s, at := todayTaskClient(t)
			added, err := c.AddNote("Atlas planning meeting")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SaveProposals(context.Background(), added.SourceID, []notes.Proposal{{ID: "owner-question", Kind: notes.Question, Text: "Who owns the Atlas release?", SourceID: added.SourceID, CreatedAt: at}}); err != nil {
				t.Fatal(err)
			}
			before, err := s.ListObservationPage(context.Background(), storage.ObservationPageRequest{Limit: 64, MaxBytes: storage.MaxObservationPageBytes})
			if err != nil {
				t.Fatal(err)
			}
			app := &taskInterleaveApp{posted: make(chan func(), 8), update: func() { c.NotifyMemoryChanging(); c.NotifyMemoryStable() }}
			d := desktop{client: c, app: app, page: "Today", today: TodayView{Date: "2026-11-12"}, answers: map[string]string{"owner-question": "Mira owns the release"}}
			if action == "answer" {
				d.answerQuestion("owner-question")
			} else {
				d.dismissQuestion("owner-question")
			}
			select {
			case <-app.posted:
			case <-time.After(5 * time.Second):
				t.Fatal("question action did not finish")
			}
			snapshot, _, err := s.GetTodaySnapshot(context.Background(), "2026-11-12")
			if err != nil || len(snapshot.Questions) != 1 || snapshot.Questions[0].Dismissed || snapshot.Questions[0].AnswerSourceID != "" {
				t.Fatal("stale question action changed the new memory epoch", snapshot.Questions, err)
			}
			after, err := s.ListObservationPage(context.Background(), storage.ObservationPageRequest{Limit: 64, MaxBytes: storage.MaxObservationPageBytes})
			if err != nil || len(before.Observations) != len(after.Observations) {
				t.Fatal("stale answer created a note after invalidation", before, after, err)
			}
		})
	}
}

func TestTodayShowsExplicitTasksWithoutCapturedSnapshot(t *testing.T) {
	c, s, now := todayTaskClient(t)
	created, err := c.CreateTask(context.Background(), "Drycleaners on Monday", &tasks.Reminder{DueAt: now.AddDate(0, 0, 4), Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := c.Today("2026-11-12")
	if err != nil || view.State != "ready" || len(view.Tasks) != 1 || view.Tasks[0].ID != created.Item.ID || len(view.Snapshot.ConfirmedTodos) != 1 {
		t.Fatalf("saved task missing without capture snapshot: %+v %v", view, err)
	}
	if !view.Tasks[0].Reminder.DueAt.Equal(now.AddDate(0, 0, 4)) {
		t.Fatal("Today changed a future schedule", view.Tasks[0])
	}
	if !strings.Contains(view.Snapshot.ConfirmedTodos[0].Text, darwin.LocalizedShortDateTime(created.Item.Reminder.DueAt, "UTC")) {
		t.Fatal("quick preview hid the compact future reminder schedule", view.Snapshot.ConfirmedTodos)
	}
	if _, found, err := s.GetTodaySnapshot(context.Background(), view.Date); err != nil || found {
		t.Fatal("reading tasks materialized a shadow snapshot", found, err)
	}
	if err := c.DeleteTask(context.Background(), created.Item.ID, created.Item.Revision); err != nil {
		t.Fatal(err)
	}
	view, err = c.Today(view.Date)
	if err != nil || len(view.Tasks) != 0 || len(view.Snapshot.ConfirmedTodos) != 0 {
		t.Fatal("Today recreated a removed explicit task", view, err)
	}
}

func TestTodaySelectsCreatedDueAndOpenUnscheduledTasks(t *testing.T) {
	c, s, now := todayTaskClient(t)
	ctx := context.Background()
	for index, spec := range []struct {
		title   string
		created time.Time
		state   tasks.State
		due     time.Time
	}{
		{"open unscheduled", now.AddDate(0, 0, -3), tasks.Open, time.Time{}},
		{"overdue", now.AddDate(0, 0, -3), tasks.Open, now.AddDate(0, 0, -1)},
		{"due today", now.AddDate(0, 0, -3), tasks.Open, now.Add(time.Hour)},
		{"future from earlier", now.AddDate(0, 0, -3), tasks.Open, now.AddDate(0, 0, 4)},
		{"completed earlier", now.AddDate(0, 0, -3), tasks.Completed, time.Time{}},
		{"completed saved today", now, tasks.Completed, time.Time{}},
	} {
		item := tasks.Item{ID: strings.Repeat("0", 31) + string(rune('a'+index)), Revision: 1, Title: spec.title, State: spec.state, CreatedAt: spec.created, UpdatedAt: spec.created, UserAuthored: true}
		if !spec.due.IsZero() {
			item.Reminder = &tasks.Reminder{DueAt: spec.due, Timezone: "UTC"}
			item.Delivery = tasks.Delivery{State: tasks.NeedsSync, At: spec.created}
		}
		if err := s.PutTask(ctx, item, 0); err != nil {
			t.Fatal(err)
		}
	}
	view, err := c.Today(now.Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, item := range view.Tasks {
		got[item.Title] = true
	}
	for _, title := range []string{"open unscheduled", "overdue", "due today", "completed saved today"} {
		if !got[title] {
			t.Fatal("relevant task missing", title, got)
		}
	}
	if len(got) != 4 || len(view.Snapshot.ConfirmedTodos) != 3 {
		t.Fatal("Today included future/old completed tasks or stale active rows", view)
	}
}

func TestCompletingTaskFromSearchRefreshesItsDisplayedState(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	created, err := c.CreateTask(context.Background(), "Drycleaners pickup", nil)
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.SearchMemoryPageContext(context.Background(), "drycleaners")
	if err != nil {
		t.Fatal(err)
	}
	app := &taskInterleaveApp{posted: make(chan func(), 8)}
	d := desktop{client: c, app: app, page: "Search", query: "drycleaners", searchHits: page.Hits, searchComplete: true}
	d.changeTaskState(created.Item, tasks.Completed)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for len(d.searchHits) != 1 || d.searchHits[0].Task == nil || d.searchHits[0].Task.State != tasks.Completed || d.loading {
		select {
		case publish := <-app.posted:
			publish()
		case <-deadline.C:
			t.Fatal("Search did not refresh after completing its task", d.searchHits)
		}
	}
}
