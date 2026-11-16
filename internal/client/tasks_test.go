package client

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

func TestQuickExplicitTaskAndReminderIntentsUseRealActions(t *testing.T) {
	for _, test := range []struct {
		input string
		kind  quickInputKind
		body  string
	}{
		{"task: Review the PR", quickInputTask, "Review the PR"},
		{"todo: Check backups", quickInputTask, "Check backups"},
		{"please remind me in 10 minutes to check backups", quickInputReminder, "remind me in 10 minutes to check backups"},
		{"can you remind me tomorrow at 9am to review the PR", quickInputReminder, "remind me tomorrow at 9am to review the PR"},
		{"remind me every friday at 16:00 to review logs", quickInputReminder, "remind me every friday at 16:00 to review logs"},
		{"remind me what I did yesterday", quickInputAsk, "remind me what I did yesterday"},
		{"can you remind me what I read about compiler releases?", quickInputAsk, "can you remind me what I read about compiler releases?"},
	} {
		kind, body := classifyQuickInput(test.input)
		if kind != test.kind || body != test.body {
			t.Fatalf("%q => %d %q", test.input, kind, body)
		}
	}
	if text := quickPanelNoteText("task: Review the PR"); text != "task: Review the PR" {
		t.Fatal("explicit save-as-note changed the original task prefix", text)
	}
}

type clientNoticePort struct {
	pending map[string]reminders.Notice
	queued  int
}

func (p *clientNoticePort) Authorization(context.Context) (reminders.Authorization, error) {
	return reminders.Allowed, nil
}
func (p *clientNoticePort) RequestAuthorization(context.Context) (reminders.Authorization, error) {
	return reminders.Allowed, nil
}
func (p *clientNoticePort) Snapshot(context.Context) (reminders.Snapshot, error) {
	s := reminders.Snapshot{}
	for id := range p.pending {
		s.Pending = append(s.Pending, id)
	}
	return s, nil
}
func (p *clientNoticePort) Queue(_ context.Context, n reminders.Notice) error {
	if p.pending == nil {
		p.pending = map[string]reminders.Notice{}
	}
	p.pending[n.ID] = n
	p.queued++
	return nil
}
func (p *clientNoticePort) Remove(_ context.Context, ids []string) error {
	for _, id := range ids {
		delete(p.pending, id)
	}
	return nil
}
func (p *clientNoticePort) RemoveAll(context.Context) error { clear(p.pending); return nil }

func TestClientTasksAndScheduledRemindersWorkWithoutAIAndDeleteCompletely(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	c := NewClient(store)
	defer c.Close()
	port := &clientNoticePort{}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	service, err := reminders.NewService(store, port, reminders.Options{Now: func() time.Time { return now }, OnChange: c.notifyTasksChanged})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	c.reminders = service
	deleter, _ := deletion.NewService(store, nil)
	deleter.SetNotificationCleanup(service)
	c.WithDeletion(deleter)
	if c.LocalAIEnabled() {
		t.Fatal("fixture unexpectedly has AI")
	}
	draft, err := reminders.Parse("remind me tomorrow at 09:00 to review the PR", now, "Europe/Brussels")
	if err != nil || draft.Missing != "" {
		t.Fatal(draft, err)
	}
	result, err := c.CreateTask(ctx, draft.Title, draft.Reminder)
	if err != nil || result.ScheduleError != nil || result.Item.Delivery.State != tasks.Scheduled || port.queued != 1 {
		t.Fatalf("reminder not actually scheduled: %+v %v", result, err)
	}
	if copy := taskResultCopy(result); !strings.HasPrefix(copy, "Scheduled:") || strings.Contains(copy, "Europe/Brussels") {
		t.Fatal("short scheduled outcome missing", copy)
	}
	page, err := c.TaskPage(ctx, "", tasks.Open)
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	completed := result.Item
	completed.State = tasks.Completed
	if _, err := c.UpdateTask(ctx, completed); err != nil {
		t.Fatal(err)
	}
	if len(port.pending) != 0 {
		t.Fatal("complete left scheduled notification")
	}
	page, err = c.TaskPage(ctx, "", tasks.Completed)
	if err != nil || len(page.Items) != 1 {
		t.Fatal("completed task not browsable", page, err)
	}
	if _, err := c.CreateTask(ctx, "Independent task", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteAll(ctx); err != nil {
		t.Fatal(err)
	}
	page, err = c.TaskPage(ctx, "", "")
	if err != nil || len(page.Items) != 0 || len(port.pending) != 0 {
		t.Fatal("delete-all left tasks or native notices", page, err)
	}
}

func TestTaskDeliveryCopyNeverClaimsUnconfirmedNotificationWasScheduled(t *testing.T) {
	item := tasks.Item{State: tasks.Open, Reminder: &tasks.Reminder{DueAt: time.Now().Add(time.Hour), Timezone: "UTC"}, Delivery: tasks.Delivery{State: tasks.Scheduling, Attempted: true}}
	copy := taskResultCopy(reminders.Result{Item: item, ScheduleError: reminders.ErrOutcomeUnknown})
	if strings.HasPrefix(copy, "Scheduled:") || !strings.Contains(copy, "could not be confirmed") {
		t.Fatal(copy)
	}
	if !strings.Contains(reminderMissingCopy("ambiguous_time"), "twice") {
		t.Fatal("DST fold clarification lost")
	}
}
