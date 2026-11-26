//go:build linux

package linux

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/tasks"
)

type fakeNotificationBackend struct {
	mu      sync.Mutex
	up      bool
	failing bool
	nextID  uint32
	shown   []string
	closed  []uint32
	eventCh chan notificationEvent
}

func newFakeNotificationBackend() *fakeNotificationBackend {
	return &fakeNotificationBackend{up: true, eventCh: make(chan notificationEvent, 4)}
}

func (f *fakeNotificationBackend) available(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.up
}

func (f *fakeNotificationBackend) show(_ context.Context, _, body string) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing {
		return 0, reminders.ErrUnavailable
	}
	f.nextID++
	f.shown = append(f.shown, body)
	return f.nextID, nil
}

func (f *fakeNotificationBackend) close(id uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, id)
}

func (f *fakeNotificationBackend) events() <-chan notificationEvent { return f.eventCh }

func (f *fakeNotificationBackend) shownTitles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.shown)
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

const testTaskID = "0123456789abcdef0123456789abcdef"

func testNoticeID(t *testing.T, revision string) string {
	t.Helper()
	id := reminders.NotificationPrefix + testTaskID + "." + revision
	if _, _, ok := reminders.NotificationTaskID(id); !ok {
		t.Fatalf("test notice id %q is not a valid reminder id", id)
	}
	return id
}

func newTestNotifications() (*Notifications, *fakeNotificationBackend, *fakeClock) {
	backend := newFakeNotificationBackend()
	clock := &fakeClock{now: time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)}
	return newNotifications(backend, clock.Now, time.Hour), backend, clock
}

func TestNotificationsDeliverOneTimeReminderOnce(t *testing.T) {
	n, backend, clock := newTestNotifications()
	ctx := context.Background()
	id := testNoticeID(t, "1")
	due := clock.Now().Add(10 * time.Minute)
	if err := n.Queue(ctx, reminders.Notice{ID: id, Title: "Call the dentist", Schedule: tasks.Reminder{DueAt: due, Timezone: "UTC"}}); err != nil {
		t.Fatalf("Queue: %v", err)
	}
	defer n.Close()
	snapshot, _ := n.Snapshot(ctx)
	if !slices.Equal(snapshot.Pending, []string{id}) || !snapshot.NextAt[id].Equal(due) || snapshot.ContentVersions[id] != reminders.NotificationContentVersion {
		t.Fatalf("pending snapshot = %+v", snapshot)
	}

	n.deliverDue()
	if got := backend.shownTitles(); len(got) != 0 {
		t.Fatalf("shown before due: %v", got)
	}
	clock.Advance(10 * time.Minute)
	n.deliverDue()
	n.deliverDue()
	if got := backend.shownTitles(); !slices.Equal(got, []string{"Call the dentist"}) {
		t.Fatalf("shown = %v, want one reminder", got)
	}
	snapshot, _ = n.Snapshot(ctx)
	if len(snapshot.Pending) != 0 || !slices.Equal(snapshot.Delivered, []string{id}) {
		t.Fatalf("delivered snapshot = %+v", snapshot)
	}
}

func TestNotificationsAdvanceRepeatingReminder(t *testing.T) {
	n, backend, clock := newTestNotifications()
	ctx := context.Background()
	id := testNoticeID(t, "2")
	first := clock.Now().Add(time.Hour)
	schedule := tasks.Reminder{DueAt: first, Timezone: "UTC", Repeat: tasks.Daily, Hour: first.Hour(), Minute: first.Minute()}
	if err := n.Queue(ctx, reminders.Notice{ID: id, Title: "Stand up", Schedule: schedule}); err != nil {
		t.Fatalf("Queue: %v", err)
	}
	defer n.Close()
	clock.Advance(time.Hour)
	n.deliverDue()
	snapshot, _ := n.Snapshot(ctx)
	if !slices.Equal(snapshot.Pending, []string{id}) || !snapshot.NextAt[id].Equal(first.AddDate(0, 0, 1)) {
		t.Fatalf("repeating snapshot = %+v", snapshot)
	}
	if got := backend.shownTitles(); !slices.Equal(got, []string{"Stand up"}) {
		t.Fatalf("shown = %v", got)
	}
}

func TestNotificationsRemoveCancelsAndClosesVisibleNotice(t *testing.T) {
	n, backend, clock := newTestNotifications()
	ctx := context.Background()
	shown, waiting := testNoticeID(t, "3"), testNoticeID(t, "4")
	for _, notice := range []reminders.Notice{
		{ID: shown, Title: "Soon", Schedule: tasks.Reminder{DueAt: clock.Now().Add(time.Minute), Timezone: "UTC"}},
		{ID: waiting, Title: "Later", Schedule: tasks.Reminder{DueAt: clock.Now().Add(time.Hour), Timezone: "UTC"}},
	} {
		if err := n.Queue(ctx, notice); err != nil {
			t.Fatalf("Queue: %v", err)
		}
	}
	defer n.Close()
	clock.Advance(time.Minute)
	n.deliverDue()
	if err := n.Remove(ctx, []string{shown, waiting}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	snapshot, _ := n.Snapshot(ctx)
	if len(snapshot.Pending)+len(snapshot.Delivered) != 0 {
		t.Fatalf("snapshot after remove = %+v", snapshot)
	}
	backend.mu.Lock()
	closed := slices.Clone(backend.closed)
	backend.mu.Unlock()
	if !slices.Equal(closed, []uint32{1}) {
		t.Fatalf("closed = %v, want the visible notice", closed)
	}
	clock.Advance(time.Hour)
	n.deliverDue()
	if got := backend.shownTitles(); !slices.Equal(got, []string{"Soon"}) {
		t.Fatalf("removed reminder was shown: %v", got)
	}
}

func TestNotificationsRejectInvalidNotices(t *testing.T) {
	n, _, clock := newTestNotifications()
	defer n.Close()
	ctx := context.Background()
	past := reminders.Notice{ID: testNoticeID(t, "5"), Title: "Late", Schedule: tasks.Reminder{DueAt: clock.Now().Add(-time.Minute), Timezone: "UTC"}}
	if err := n.Queue(ctx, past); !errors.Is(err, tasks.ErrInvalid) {
		t.Fatalf("past one-time reminder = %v, want invalid", err)
	}
	foreign := reminders.Notice{ID: "other.app.notice", Title: "X", Schedule: tasks.Reminder{DueAt: clock.Now().Add(time.Minute), Timezone: "UTC"}}
	if err := n.Queue(ctx, foreign); !errors.Is(err, tasks.ErrInvalid) {
		t.Fatalf("foreign id = %v, want invalid", err)
	}
	if err := n.Remove(ctx, []string{"other.app.notice"}); !errors.Is(err, tasks.ErrInvalid) {
		t.Fatalf("foreign remove = %v, want invalid", err)
	}
}

func TestNotificationsAuthorizationFollowsService(t *testing.T) {
	n, backend, _ := newTestNotifications()
	ctx := context.Background()
	if auth, err := n.RequestAuthorization(ctx); err != nil || auth != reminders.Allowed {
		t.Fatalf("available service = %v, %v", auth, err)
	}
	backend.mu.Lock()
	backend.up = false
	backend.mu.Unlock()
	if auth, err := n.Authorization(ctx); err != nil || auth != reminders.Unsupported {
		t.Fatalf("missing service = %v, %v", auth, err)
	}
}

func TestNotificationsSchedulerShowsDueReminderAndReportsClick(t *testing.T) {
	backend := newFakeNotificationBackend()
	n := newNotifications(backend, time.Now, time.Hour)
	defer n.Close()
	id := testNoticeID(t, "6")
	if err := n.Queue(context.Background(), reminders.Notice{ID: id, Title: "Water plants", Schedule: tasks.Reminder{DueAt: time.Now().Add(1500 * time.Millisecond), Timezone: "UTC"}}); err != nil {
		t.Fatalf("Queue: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(backend.shownTitles()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("scheduler did not show the due reminder")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Wait until the native ID is recorded before activating it.
	for {
		n.mu.Lock()
		_, recorded := n.shown[1]
		n.mu.Unlock()
		if recorded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shown reminder was not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	backend.eventCh <- notificationEvent{id: 1, activated: true}
	select {
	case got := <-n.Opened():
		if got != testTaskID {
			t.Fatalf("opened task = %q, want %q", got, testTaskID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("click was not reported")
	}
}

func TestNotificationsDoNotReportUnshownReminderAsDelivered(t *testing.T) {
	n, backend, clock := newTestNotifications()
	defer n.Close()
	ctx := context.Background()
	id := testNoticeID(t, "7")
	if err := n.Queue(ctx, reminders.Notice{ID: id, Title: "Pay rent", Schedule: tasks.Reminder{DueAt: clock.Now().Add(time.Minute), Timezone: "UTC"}}); err != nil {
		t.Fatalf("Queue: %v", err)
	}
	backend.mu.Lock()
	backend.failing = true
	backend.mu.Unlock()
	clock.Advance(time.Minute)
	n.deliverDue()
	snapshot, _ := n.Snapshot(ctx)
	if len(snapshot.Pending) != 0 || len(snapshot.Delivered) != 0 {
		t.Fatalf("failed reminder snapshot = %+v, want neither pending nor delivered", snapshot)
	}
}
