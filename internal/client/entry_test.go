package client

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

func TestChangedTaskTargetDoesNotBlockNewQuestionsOrAuthorizeStaleEdits(t *testing.T) {
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
	created, err := c.CreateTask(ctx, "Review PR", nil)
	if err != nil {
		t.Fatal(err)
	}
	epoch, _ := c.BeginMemoryRead()
	state := EntryState{Epoch: epoch, Targets: []assistant.TargetRef{{Kind: "task", ID: created.Item.ID, Revision: created.Item.Revision, Label: "T1"}}}
	changed := created.Item
	changed.Title = "Review security PR"
	if _, err := c.UpdateTask(ctx, changed); err != nil {
		t.Fatal(err)
	}
	resolved, err := c.ResolveEntry(ctx, "ask: Explain Go interfaces", state, "UTC")
	if err != nil || resolved.Action.Kind != intent.Ask || resolved.Action.Text != "Explain Go interfaces" {
		t.Fatal("an unrelated question was blocked by an older task reference", resolved, err)
	}
	resolved, err = c.ResolveEntry(ctx, "cancel it", state, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ApplyTaskEntry(ctx, resolved, "UTC"); !errors.Is(err, tasks.ErrConflict) {
		t.Fatal("the original action reference silently acquired a new revision", err)
	}
	item, err := c.Task(ctx, created.Item.ID)
	if err != nil || item.State != tasks.Open || item.Title != "Review security PR" {
		t.Fatal("a stale action changed the task", item, err)
	}
}

func TestOfflineReminderFollowupReadsCurrentTaskWithInspectableEvidence(t *testing.T) {
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
	port := &clientNoticePort{}
	service, err := reminders.NewService(s, port, reminders.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	c.reminders = service
	created, err := c.CreateTask(ctx, "Check backups", &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	epoch, _ := c.BeginMemoryRead()
	state := EntryState{Epoch: epoch, Targets: []assistant.TargetRef{{Kind: "task", ID: created.Item.ID, Revision: created.Item.Revision, Label: "T1"}}}
	changed := created.Item
	changed.Title = "Check encrypted backups"
	changed.Reminder.DueAt = now.Add(2 * time.Hour)
	updated, err := c.UpdateTask(ctx, changed)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := c.ResolveEntry(ctx, "When is it due?", state, "UTC")
	if err != nil || resolved.Action.Kind != intent.TaskInspect || resolved.Answer == nil {
		t.Fatal("the follow-up lost the task just created", resolved, err)
	}
	answer := resolved.Answer
	if !answer.Direct || answer.Answer.InsufficientEvidence || len(answer.Sources) != 1 || answer.Sources[0].Origin != assistant.OriginTask || len(answer.Targets) != 1 || answer.Targets[0].Revision != updated.Item.Revision || !strings.Contains(answer.Answer.Text, "Check encrypted backups") || !strings.Contains(answer.Answer.Text, reminderScheduleCopy(*updated.Item.Reminder)) || !strings.Contains(answer.Sources[0].Text, "Observed next queued trigger:") || !strings.Contains(answer.Sources[0].Text, "2026-11-12T14:00:00Z") {
		t.Fatal("task inspection did not expose its current schedule and provenance", answer)
	}
	if len(port.pending) != 1 || port.queued != 2 {
		t.Fatal("reading the task changed native scheduling", port)
	}
	state.Targets = append(state.Targets, assistant.TargetRef{Kind: "task", ID: created.Item.ID, Revision: created.Item.Revision, Label: "T2"})
	resolved, err = c.ResolveEntry(ctx, "When is it due?", state, "UTC")
	if err != nil || resolved.Action.Kind != intent.Clarify || resolved.Answer != nil {
		t.Fatal("an ambiguous follow-up guessed a task", resolved, err)
	}
}

func TestEntryClarificationCreatesOneReminderAndFollowupsEditAndCancel(t *testing.T) {
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
	port := &clientNoticePort{}
	service, _ := reminders.NewService(s, port, reminders.Options{Now: func() time.Time { return now }})
	defer service.Close()
	c.reminders = service
	state := EntryState{}
	resolved, err := c.ResolveEntry(ctx, "remind me tomorrow at 5 to review the PR", state, "UTC")
	if err != nil || resolved.Draft == nil || resolved.Draft.Missing != "time" {
		t.Fatal(resolved, err)
	}
	state.Epoch, state.Pending = resolved.Epoch, resolved.Draft
	resolved, err = c.ResolveEntry(ctx, "at 9am", state, "UTC")
	if err != nil || resolved.Draft == nil || resolved.Draft.Missing != "" || resolved.Draft.Title != "review the PR" {
		t.Fatal(resolved, err)
	}
	created, err := c.createTaskAtEpoch(ctx, resolved.Draft.Title, resolved.Draft.Reminder, "11111111111111111111111111111111", resolved.Epoch)
	if err != nil || created.Item.Delivery.State != tasks.Scheduled {
		t.Fatal(created, err)
	}
	state = EntryState{Epoch: resolved.Epoch, Targets: []assistant.TargetRef{{Kind: "task", ID: created.Item.ID, Revision: created.Item.Revision, Label: "T1"}}}
	resolved, err = c.ResolveEntry(ctx, "move it to Friday", state, "UTC")
	if err != nil || resolved.Action.Kind != intent.TaskEdit {
		t.Fatal(resolved, err)
	}
	updated, err := c.ApplyTaskEntry(ctx, resolved, "UTC")
	if err != nil || updated.Draft != nil || updated.Result.Item.Reminder.DueAt.Format(time.RFC3339) != "2026-11-13T09:00:00Z" || len(port.pending) != 1 {
		t.Fatal(updated, err)
	}
	state.Targets[0].Revision = updated.Result.Item.Revision
	resolved, err = c.ResolveEntry(ctx, "cancel that", state, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	updated, err = c.ApplyTaskEntry(ctx, resolved, "UTC")
	if err != nil || updated.Result.Item.State != tasks.Cancelled || len(port.pending) != 0 {
		t.Fatal(updated, err)
	}
}

func TestPersonalTaskEntryUsesAuthorizedDefaultAndSchedulesExactlyOneReminder(t *testing.T) {
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
	port := &clientNoticePort{}
	service, err := reminders.NewService(s, port, reminders.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	c.reminders = service
	resolved, err := c.ResolveEntry(ctx, "I need to go to the drycleaners on monday", EntryState{}, "Europe/Brussels")
	if err != nil || resolved.Action.Kind != intent.Reminder || resolved.Draft == nil || resolved.Draft.Missing != "" || resolved.Draft.Title != "go to the drycleaners" || resolved.Draft.Reminder == nil || resolved.Draft.Reminder.DueAt.Format(time.RFC3339) != "2026-11-16T08:00:00Z" {
		t.Fatalf("personal task did not use the authorized default: %+v, %v", resolved, err)
	}
	if port.queued != 0 || len(port.pending) != 0 {
		t.Fatal("uncommitted reminder reached notification delivery", port)
	}
	state := EntryState{Epoch: resolved.Epoch, Pending: resolved.Draft}
	resolved, err = c.ResolveEntry(ctx, "9am", state, "Europe/Brussels")
	if err != nil || resolved.Draft == nil || resolved.Draft.Missing != "" || resolved.Draft.Reminder == nil || resolved.Draft.Reminder.DueAt.Format(time.RFC3339) != "2026-11-16T08:00:00Z" {
		t.Fatalf("time reply did not retain Monday: %+v, %v", resolved, err)
	}
	created, err := c.createTaskAtEpoch(ctx, resolved.Draft.Title, resolved.Draft.Reminder, "33333333333333333333333333333333", resolved.Epoch)
	if err != nil || created.Item.Title != "go to the drycleaners" || created.Item.Delivery.State != tasks.Scheduled || port.queued != 1 || len(port.pending) != 1 {
		t.Fatalf("completed draft did not schedule one reminder: %+v, %v, %+v", created, err, port)
	}
}

func TestEntryWillNotGuessBetweenTasksOrActAfterMemoryDeletion(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	epoch, _ := c.BeginMemoryRead()
	c.NotifyMemoryChanging()
	c.NotifyMemoryStable()
	_, err := c.ResolveEntry(context.Background(), "cancel it", EntryState{Epoch: epoch, Targets: []assistant.TargetRef{{Kind: "task", ID: "11111111111111111111111111111111", Revision: 1, Label: "T1"}}}, "UTC")
	if err != ErrMemoryChanging {
		t.Fatal("stale action survived deletion", err)
	}
}

func TestEntryNoteFenceRejectsPreDeletionSubmission(t *testing.T) {
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	c := NewClient(s).WithNotes(notes.NewService(s, nil, nil))
	defer c.Close()
	epoch, _ := c.BeginMemoryRead()
	c.NotifyMemoryChanging()
	c.NotifyMemoryStable()
	if _, err := c.addNoteAtEpoch(context.Background(), "A stale submitted note.", epoch); err != ErrMemoryChanging {
		t.Fatal("old note resurrected after deletion", err)
	}
}

func TestMemoryInvalidationClearsEntryTargetsTranscriptAndSavedNoteHistory(t *testing.T) {
	canceled := false
	d := desktop{quickEntry: EntryState{Targets: []assistant.TargetRef{{Kind: "task", ID: "11111111111111111111111111111111", Revision: 1}}}, quickConversation: []quickConversationTurn{{answer: "Private captured answer."}}, quickEntryLoading: true, quickEntryCancel: func() { canceled = true }}
	d.quickHistory.Record("Private saved note.")
	d.invalidateMemoryViews()
	if !canceled || d.quickEntryLoading || len(d.quickEntry.Targets) != 0 || len(d.quickConversation) != 0 || len(d.quickHistory.entries) != 0 {
		t.Fatal("deleted evidence or action context remained in the panel")
	}
}

func TestRecurringEntryDateEditAsksBeforeRemovingRecurrence(t *testing.T) {
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
	port := &clientNoticePort{}
	service, _ := reminders.NewService(s, port, reminders.Options{Now: func() time.Time { return now }})
	defer service.Close()
	c.reminders = service
	draft, err := reminders.Parse("remind me every day at 9am to check backups", now, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	created, err := c.CreateTask(ctx, draft.Title, draft.Reminder)
	if err != nil {
		t.Fatal(err)
	}
	epoch, _ := c.BeginMemoryRead()
	resolved, err := c.ResolveEntry(ctx, "move it to Friday", EntryState{Epoch: epoch, Targets: []assistant.TargetRef{{Kind: "task", ID: created.Item.ID, Revision: created.Item.Revision, Label: "T1"}}}, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	applied, err := c.ApplyTaskEntry(ctx, resolved, "UTC")
	if err != nil || applied.Draft == nil || applied.Draft.Missing != "recurrence" || port.queued != 1 {
		t.Fatal("recurring pattern was silently removed", applied, err)
	}
}

func TestTaskDeletionCancelsReadersAndInvalidatesTaskEvidence(t *testing.T) {
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	c := NewClient(s)
	defer c.Close()
	service, _ := reminders.NewService(s, &clientNoticePort{}, reminders.Options{})
	defer service.Close()
	c.reminders = service
	created, err := c.CreateTask(ctx, "Private task", nil)
	if err != nil {
		t.Fatal(err)
	}
	epoch, _ := c.BeginMemoryRead()
	readCtx, cancel := context.WithCancel(ctx)
	id, _ := c.registerAsk(cancel)
	defer c.unregisterAsk(id)
	invalidated := false
	c.SetMemoryInvalidator(func() { invalidated = true })
	if err := c.DeleteTask(ctx, created.Item.ID, created.Item.Revision); err != nil {
		t.Fatal(err)
	}
	if readCtx.Err() == nil || c.MemoryReadCurrent(epoch) || !invalidated || c.MemoryIsChanging() {
		t.Fatal("deleted task evidence could still publish")
	}
}
