package reminders_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

type reminderDeletionStore struct {
	*durable.Store
	beforeErase func()
}

func (s *reminderDeletionStore) DeleteMeeting(ctx context.Context, id string) error {
	if s.beforeErase != nil {
		s.beforeErase()
	}
	return s.Store.DeleteMeeting(ctx, id)
}

func reminderMeeting(t *testing.T, repo *durable.Store, now time.Time) (string, string) {
	t.Helper()
	ctx := context.Background()
	m := meetings.Meeting{ID: "meeting-to-delete", Title: "Saved agenda", Locale: "en-US", StartedAt: now, State: meetings.Ready, Tracks: []string{"microphone"}, Revision: 1}
	if err := repo.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	r, err := repo.Append(ctx, meetings.Record{Key: m.ID + "/note/one", SessionID: m.ID, Kind: meetings.UserNote, Note: meetings.ManualNoteInput{ID: "one", At: time.Second, Text: "Saved agenda reminder source"}})
	if err != nil {
		t.Fatal(err)
	}
	return m.ID, r.SourceID
}

func TestMeetingDeletionPreservesUnrelatedNativeRemindersAcrossDueTime(t *testing.T) {
	ctx := context.Background()
	repo := store(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	meetingID, sourceID := reminderMeeting(t, repo, now)
	port := &fakeNotifications{auth: reminders.Allowed}
	service, err := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	create := func(title, source string) tasks.Item {
		t.Helper()
		r, err := service.Create(ctx, title, &tasks.Reminder{DueAt: now.Add(time.Second), Timezone: "UTC"}, source, false)
		if err != nil || r.ScheduleError != nil {
			t.Fatal(r, err)
		}
		return r.Item
	}
	unrelated := create("Unrelated bill", "")
	delivered := create("Previously delivered alert", "")
	linked := create("Meeting-linked reminder", sourceID)
	delete(port.pending, reminders.NotificationID(delivered))
	port.delivered = map[string]bool{reminders.NotificationID(delivered): true}
	wantPending := port.pending[reminders.NotificationID(unrelated)]
	wrapper := &reminderDeletionStore{Store: repo, beforeErase: func() { now = now.Add(2 * time.Second) }}
	deleter, err := deletion.NewService(wrapper, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleter.SetNotificationCleanup(service)
	if err := deleter.DeleteMeeting(ctx, meetingID); err != nil {
		t.Fatal(err)
	}
	if got, present := port.pending[reminders.NotificationID(unrelated)]; !present || !reflect.DeepEqual(got, wantPending) || !port.delivered[reminders.NotificationID(delivered)] || len(port.pending) != 1 || len(port.delivered) != 1 || port.queues != 3 {
		t.Fatalf("meeting deletion changed unrelated native pending/delivered content: pending=%+v delivered=%+v queues=%d", port.pending, port.delivered, port.queues)
	}
	if got, found, err := repo.GetTask(ctx, unrelated.ID); err != nil || !found || !reflect.DeepEqual(got, unrelated) {
		t.Fatalf("deletion mutated unrelated durable intent/attempt receipt: got=%+v want=%+v found=%t err=%v", got, unrelated, found, err)
	}
	if _, found, err := repo.GetTask(ctx, linked.ID); err != nil || found {
		t.Fatal("meeting-linked task survived erasure", found, err)
	}
	runPass(t, service)
	if port.queues != 3 || port.pending[reminders.NotificationID(unrelated)].ID == "" {
		t.Fatal("due one-shot was lost/replayed after deletion", port.queues, port.pending)
	}
}

func TestMeetingDeletionAbortsBeforeErasureOnNativeCleanupFailure(t *testing.T) {
	ctx := context.Background()
	repo := store(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	meetingID, sourceID := reminderMeeting(t, repo, now)
	port := &fakeNotifications{auth: reminders.Allowed}
	service, _ := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	defer service.Close()
	for _, source := range []string{"", sourceID} {
		r, err := service.Create(ctx, "Private reminder", &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "UTC"}, source, false)
		if err != nil || r.ScheduleError != nil {
			t.Fatal(r, err)
		}
	}
	deleter, _ := deletion.NewService(repo, nil)
	deleter.SetNotificationCleanup(service)
	port.removeError = reminders.ErrUnavailable
	if err := deleter.DeleteMeeting(ctx, meetingID); !errors.Is(err, reminders.ErrUnavailable) {
		t.Fatal("native cancellation failure did not abort erasure", err)
	}
	if _, found, err := repo.GetMeeting(ctx, meetingID); err != nil || !found || len(port.pending) != 2 {
		t.Fatal("failed native cleanup erased sources or unrelated notices", found, err, port.pending)
	}
}

type guardedReminderPort struct {
	*fakeNotifications
	deleting atomic.Bool
}

func (p *guardedReminderPort) Queue(ctx context.Context, notice reminders.Notice) error {
	if p.deleting.Load() {
		return errors.New("reminder queued inside deletion guard")
	}
	return p.fakeNotifications.Queue(ctx, notice)
}

func TestMeetingDeletionHoldsSchedulingUntilErasureFinishes(t *testing.T) {
	ctx := context.Background()
	repo := store(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	meetingID, _ := reminderMeeting(t, repo, now)
	port := &guardedReminderPort{fakeNotifications: &fakeNotifications{auth: reminders.Allowed}}
	service, _ := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	defer service.Close()
	entered, resume := make(chan struct{}), make(chan struct{})
	wrapper := &reminderDeletionStore{Store: repo, beforeErase: func() {
		port.deleting.Store(true)
		close(entered)
		<-resume
		port.deleting.Store(false)
	}}
	deleter, _ := deletion.NewService(wrapper, nil)
	deleter.SetNotificationCleanup(service)
	erased := make(chan error, 1)
	go func() { erased <- deleter.DeleteMeeting(ctx, meetingID) }()
	<-entered
	queued := make(chan reminders.Result, 1)
	queueErr := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		result, err := service.Create(ctx, "Concurrent independent reminder", &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "UTC"}, "", false)
		queued <- result
		queueErr <- err
	}()
	<-started
	select {
	case result := <-queued:
		close(resume)
		<-erased
		t.Fatal("scheduling escaped the held deletion guard", result, <-queueErr)
	case <-time.After(25 * time.Millisecond):
	}
	close(resume)
	if err := <-erased; err != nil {
		t.Fatal(err)
	}
	result, err := <-queued, <-queueErr
	if err != nil || result.ScheduleError != nil || port.queues != 1 || len(port.pending) != 1 {
		t.Fatal("guard did not resume concurrent independent scheduling", result, err, port.queues)
	}
}
