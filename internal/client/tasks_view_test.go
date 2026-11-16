package client

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

// Replace only the GUI dispatcher. Client operations and encrypted storage are
// real; the hook inserts deletion between the UI click and its worker starting.
type taskInterleaveApp struct {
	ui.App
	update func()
	posted chan func()
}

func (a *taskInterleaveApp) RequestUpdate() {
	if a.update != nil {
		f := a.update
		a.update = nil
		f()
	}
}
func (a *taskInterleaveApp) Post(f func()) { a.posted <- f }

func TestTaskPageClickCannotWriteAfterMemoryInvalidation(t *testing.T) {
	for _, operation := range []string{"create", "edit", "complete"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func(close func() error) { _ = close() }(s.Close)
			c := NewClient(s)
			defer c.Close()
			service, err := reminders.NewService(s, &clientNoticePort{}, reminders.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()
			c.reminders = service
			var item tasks.Item
			if operation != "create" {
				created, err := c.CreateTask(ctx, "Original title", nil)
				if err != nil {
					t.Fatal(err)
				}
				item = created.Item
			}
			app := &taskInterleaveApp{posted: make(chan func(), 1), update: func() {
				c.NotifyMemoryChanging()
				c.NotifyMemoryStable()
			}}
			d := desktop{app: app, client: c, taskTitle: "Prepared before deletion"}
			switch operation {
			case "create":
				d.saveTaskEditor()
			case "edit":
				d.taskEditor = &item
				d.saveTaskEditor()
			case "complete":
				d.changeTaskState(item, tasks.Completed)
			}
			select {
			case <-app.posted:
			case <-time.After(5 * time.Second):
				t.Fatal("task worker did not finish")
			}
			page, err := c.TaskPage(ctx, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if operation == "create" {
				if len(page.Items) != 0 {
					t.Fatal("pre-deletion form created a task after invalidation", page.Items)
				}
			} else if len(page.Items) != 1 || page.Items[0].Title != "Original title" || page.Items[0].State != tasks.Open || page.Items[0].Revision != item.Revision {
				t.Fatal("pre-deletion task action changed the new memory epoch", page.Items)
			}
		})
	}
}

func TestTaskEditFromTodayOpensTheTaskEditorPage(t *testing.T) {
	d := desktop{page: "Today", app: &taskInterleaveApp{}}
	d.openTaskEditor(tasks.Item{ID: "11111111111111111111111111111111", Revision: 1, Title: "Review Atlas", State: tasks.Open})
	if d.page != "Tasks" || !d.taskEditing || d.taskEditor == nil || d.taskTitle != "Review Atlas" {
		t.Fatal("Today task edit remained hidden on another page")
	}
}

func TestTaskSourceReadDoesNotOverrideLaterNavigation(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	added, err := c.AddNote("TODO: Review Atlas")
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
	app := &taskInterleaveApp{posted: make(chan func(), 8)}
	d := desktop{client: c, app: app, page: "Today"}
	d.inspectTaskSources(page.Items[0])
	// Navigate after clicking but before the background result is published.
	d.page = "Settings"
	select {
	case publish := <-app.posted:
		publish()
	case <-time.After(5 * time.Second):
		t.Fatal("source read did not finish")
	}
	if d.page != "Settings" || d.loading || d.askLoading {
		t.Fatal("source read took over later navigation", d.page, d.loading, d.askLoading)
	}
	if d.askResult == nil || len(d.askResult.Sources) != 1 {
		t.Fatal("source result was lost")
	}
}

func TestReturningToTodayRefreshesTaskState(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	app := &taskInterleaveApp{posted: make(chan func(), 8)}
	d := desktop{client: c, app: app, page: "Tasks", date: "2026-11-12"}
	d.navigatePage("Today")
	if !d.todayLoading {
		t.Fatal("Today reused a stale task projection after navigation")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for d.todayLoading || d.hourlyLoading {
		select {
		case publish := <-app.posted:
			publish()
		case <-deadline.C:
			t.Fatal("Today refresh did not finish")
		}
	}
	if d.today.Date != d.date {
		t.Fatal("selected date did not refresh", d.today.Date)
	}
}
