package client

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

func TestOfflineTaskQuestionsAndCompletionHistory(t *testing.T) {
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	c := NewClient(s)
	defer c.Close()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	c.askNow = func() time.Time { return now }
	service, err := reminders.NewService(s, &clientNoticePort{}, reminders.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	c.reminders = service
	created, err := c.CreateTask(ctx, "Review PR", nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.AskMemory(ctx, "What is on my task list?")
	if err != nil || !result.Direct || !strings.Contains(result.Answer.Text, "Review PR") || len(result.Targets) != 1 || result.Targets[0].ID != created.Item.ID {
		t.Fatal(result, err)
	}
	result, err = c.AskMemory(ctx, "What do I need to do?")
	if err != nil || !result.Direct || !strings.Contains(result.Answer.Text, "Review PR") || len(result.Targets) != 1 || result.Targets[0].ID != created.Item.ID {
		t.Fatal("natural task question did not list current open work", result, err)
	}
	item := created.Item
	item.State = tasks.Completed
	completed, err := c.UpdateTask(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	item = completed.Item
	item.Title = "Review deployment PR"
	item.State = tasks.Open
	if _, err := c.UpdateTask(ctx, item); err != nil {
		t.Fatal(err)
	}
	result, err = c.AskMemory(ctx, "Which tasks did I complete yesterday?")
	if err != nil || !result.Direct || !strings.Contains(result.Answer.Text, "Review deployment PR") || len(result.Targets) != 1 || !strings.Contains(result.Answer.Text, "12 Nov") {
		t.Fatal("completion event lost after reopen/title edit", result, err)
	}
	result, err = c.AskMemory(ctx, "How many open tasks do I have?")
	if err != nil || !result.Direct || !strings.Contains(result.Answer.Text, "1 open task") {
		t.Fatal(result, err)
	}
}

func TestTaskQueriesRespectNextReminderOrderAndDoNotStealRecall(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	if _, ok := parseTaskQuery("What did I read about reminders yesterday?", now); ok {
		t.Fatal("captured recall became task inspection")
	}
	query, ok := parseTaskQuery("What's my next reminder?", now)
	if !ok || !query.Next || !query.RemindersOnly {
		t.Fatal(query, ok)
	}
	if _, ok := parseTaskQuery("Which tasks could improve Go code quality?", now); ok {
		t.Fatal("general brainstorming became current task inspection")
	}
}

func TestPersonalTaskAndDayQuestionsGetSourceBackedTaskContext(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	c := NewClient(s)
	defer c.Close()
	c.askNow = func() time.Time { return now }
	service, _ := reminders.NewService(s, &clientNoticePort{}, reminders.Options{Now: func() time.Time { return now }})
	defer service.Close()
	c.reminders = service
	created, err := c.CreateTask(ctx, "Review the deployment PR", nil)
	if err != nil {
		t.Fatal(err)
	}
	context, err := c.PersonalAnswerContext(ctx, assistant.Plan{Kind: assistant.KindLookup, Question: "What should I work on next?"})
	if err != nil || len(context.Facts) != 1 || context.Facts[0].Origin != assistant.OriginTask || !strings.Contains(context.Facts[0].Text, "Review the deployment PR") {
		t.Fatal(context, err)
	}
	item := created.Item
	item.State = tasks.Completed
	if _, err := c.UpdateTask(ctx, item); err != nil {
		t.Fatal(err)
	}
	context, err = c.PersonalAnswerContext(ctx, assistant.Plan{Kind: assistant.KindRecap, Question: "What did I do today?", Scope: assistant.Scope{Start: now.Add(-time.Hour), End: now.Add(time.Hour)}})
	if err != nil || len(context.Facts) != 1 || context.Facts[0].MatchEnd == 0 || !strings.Contains(context.Facts[0].Text[:context.Facts[0].MatchEnd], "completed") {
		t.Fatal(context, err)
	}
}

func TestTaskEvidenceIncludesObservedQueueTimeAndBindsReceiptSnapshot(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	c := NewClient(s)
	defer c.Close()
	c.askNow = func() time.Time { return now }
	service, _ := reminders.NewService(s, &clientNoticePort{}, reminders.Options{Now: func() time.Time { return now }})
	defer service.Close()
	c.reminders = service
	created, err := c.CreateTask(ctx, "Check backups", &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := c.readTaskQuery(ctx, taskQuery{State: tasks.Open})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskDelivery(ctx, created.Item.ID, created.Item.Revision, tasks.Delivery{State: tasks.Scheduled, At: now, Attempted: true, NextAt: now.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	after, err := c.readTaskQuery(ctx, taskQuery{State: tasks.Open})
	queued := tasks.Reminder{DueAt: now.Add(2 * time.Hour), Timezone: "UTC"}
	if err != nil || len(after.Sources) != 1 || !strings.Contains(after.Sources[0].Text, "Observed next queued trigger: "+reminderEvidenceTimeCopy(queued)) || before.SourceIDs[0] == after.SourceIDs[0] {
		t.Fatal("citation lost the actual native receipt", after, err)
	}
}
