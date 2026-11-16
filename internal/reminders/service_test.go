package reminders_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

type fakeNotifications struct {
	mu            sync.Mutex
	auth          reminders.Authorization
	pending       map[string]reminders.Notice
	delivered     map[string]bool
	queues        int
	queueError    error
	removeError   error
	snapshotError error
	versionAware  bool
}

func (p *fakeNotifications) Authorization(context.Context) (reminders.Authorization, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.auth, nil
}
func (p *fakeNotifications) RequestAuthorization(context.Context) (reminders.Authorization, error) {
	return p.Authorization(context.Background())
}
func (p *fakeNotifications) Snapshot(context.Context) (reminders.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snapshotError != nil {
		return reminders.Snapshot{}, p.snapshotError
	}
	s := reminders.Snapshot{}
	if p.versionAware {
		s.ContentVersions = make(map[string]uint8)
	}
	for id, notice := range p.pending {
		s.Pending = append(s.Pending, id)
		if p.versionAware {
			s.ContentVersions[id] = 0
			if tasks.ValidTitle(notice.Title) {
				s.ContentVersions[id] = reminders.NotificationContentVersion
			}
		}
	}
	for id := range p.delivered {
		s.Delivered = append(s.Delivered, id)
	}
	return s, nil
}
func (p *fakeNotifications) Queue(_ context.Context, n reminders.Notice) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queues++
	if p.queueError != nil {
		return p.queueError
	}
	if p.pending == nil {
		p.pending = map[string]reminders.Notice{}
	}
	p.pending[n.ID] = n
	return nil
}
func (p *fakeNotifications) Remove(_ context.Context, ids []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.removeError != nil {
		return p.removeError
	}
	for _, id := range ids {
		delete(p.pending, id)
		delete(p.delivered, id)
	}
	return nil
}
func (p *fakeNotifications) RemoveAll(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.removeError != nil {
		return p.removeError
	}
	clear(p.pending)
	clear(p.delivered)
	return nil
}

func store(t *testing.T) *durable.Store {
	t.Helper()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func runPass(t *testing.T, s *reminders.Service) {
	t.Helper()
	for i := 0; i < 20; i++ {
		err := s.Reconcile(context.Background())
		if err == nil {
			return
		}
		if !errors.Is(err, reminders.ErrMoreWork) {
			t.Fatal(err)
		}
	}
	t.Fatal("reminder recovery did not finish")
}

func TestReminderNativeQueueRestartEditCancelAndObservedDelivery(t *testing.T) {
	repo := store(t)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	port := &fakeNotifications{auth: reminders.Allowed}
	s, err := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Create(context.Background(), "Review the private deployment", &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "Europe/Brussels"}, "", false)
	if err != nil || result.ScheduleError != nil || result.Item.Delivery.State != tasks.Scheduled {
		t.Fatalf("not actually queued: %+v %v", result, err)
	}
	if !result.Item.Delivery.NextAt.Equal(result.Item.Reminder.DueAt) {
		t.Fatal("resolved native delivery time not persisted", result.Item.Delivery)
	}
	id := reminders.NotificationID(result.Item)
	if port.queues != 1 || len(port.pending) != 1 {
		t.Fatal("queue not called once")
	}
	s.Close()
	s, _ = reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	defer s.Close()
	runPass(t, s)
	if port.queues != 1 {
		t.Fatal("restart duplicated already pending reminder")
	}
	edited := result.Item
	edited.Title = "Review the deployment after lunch"
	edited.Reminder = &tasks.Reminder{DueAt: now.Add(2 * time.Hour), Timezone: "Europe/Brussels"}
	updated, err := s.Update(context.Background(), edited, false)
	if err != nil || updated.ScheduleError != nil {
		t.Fatal(updated, err)
	}
	if _, exists := port.pending[id]; exists || len(port.pending) != 1 {
		t.Fatal("edit kept stale scheduled notification")
	}
	id = reminders.NotificationID(updated.Item)
	delete(port.pending, id)
	port.delivered = map[string]bool{id: true}
	now = now.Add(3 * time.Hour)
	runPass(t, s)
	got, _, _ := repo.GetTask(context.Background(), updated.Item.ID)
	if got.Delivery.State != tasks.Delivered {
		t.Fatal("delivery not observed", got.Delivery)
	}
	got.State = tasks.Cancelled
	if _, err := s.Update(context.Background(), got, false); err != nil {
		t.Fatal(err)
	}
	if len(port.delivered) != 0 || len(port.pending) != 0 {
		t.Fatal("cancel left notifications visible or pending")
	}
}

func TestPermissionDeniedPersistsIntentWithoutClaimingSchedule(t *testing.T) {
	repo := store(t)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	port := &fakeNotifications{auth: reminders.Denied}
	s, _ := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	defer s.Close()
	result, err := s.Create(context.Background(), "Check backups", &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "UTC"}, "", true)
	if err != nil || !errors.Is(result.ScheduleError, reminders.ErrPermission) || result.Item.Delivery.State != tasks.Blocked || port.queues != 0 {
		t.Fatalf("denied reminder falsely scheduled: %+v %v", result, err)
	}
	port.auth = reminders.Allowed
	runPass(t, s)
	got, _, _ := repo.GetTask(context.Background(), result.Item.ID)
	if got.Delivery.State != tasks.Scheduled || port.queues != 1 {
		t.Fatal("permission grant did not recover intent", got.Delivery)
	}
}

func TestUncertainPastDeliveryDoesNotAlertAgainAfterRestart(t *testing.T) {
	repo := store(t)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	port := &fakeNotifications{auth: reminders.Allowed, queueError: reminders.ErrOutcomeUnknown}
	s, _ := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	result, err := s.Create(context.Background(), "Private reminder", &tasks.Reminder{DueAt: now.Add(time.Minute), Timezone: "UTC"}, "", false)
	if err != nil || result.ScheduleError == nil {
		t.Fatal(result, err)
	}
	s.Close()
	now = now.Add(2 * time.Minute)
	port.queueError = nil
	s, _ = reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	defer s.Close()
	runPass(t, s)
	got, _, _ := repo.GetTask(context.Background(), result.Item.ID)
	if port.queues != 1 || got.Delivery.State != tasks.Elapsed {
		t.Fatal("ambiguous prior delivery was repeated or reported delivered", port.queues, got.Delivery)
	}
}

func TestSourceRetentionAndDeleteAllCancelNativeBeforeErasure(t *testing.T) {
	repo := store(t)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	port := &fakeNotifications{auth: reminders.Allowed}
	s, _ := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	defer s.Close()
	deleter, _ := deletion.NewService(repo, nil)
	deleter.SetNotificationCleanup(s)
	o, _ := capture.SanitizeObservation("sh.blur.notes", "local", "Review deployment reminder source", 4096)
	if err := repo.PutObservationAt(context.Background(), o, now); err != nil {
		t.Fatal(err)
	}
	result, err := s.Create(context.Background(), "Review deployment", &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "UTC"}, o.ContentID(), false)
	if err != nil || result.ScheduleError != nil {
		t.Fatal(result, err)
	}
	port.removeError = reminders.ErrUnavailable
	if _, err := deleter.DeleteSource(context.Background(), o.ContentID()); !errors.Is(err, reminders.ErrUnavailable) {
		t.Fatal("erasure ignored cancellation failure", err)
	}
	if _, found, err := repo.GetObservation(context.Background(), o.ContentID()); err != nil || !found {
		t.Fatal("source erased while its notice remained queued", found, err)
	}
	port.removeError = nil
	if _, err := deleter.DeleteBefore(context.Background(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repo.GetTask(context.Background(), result.Item.ID); err != nil || found || len(port.pending) != 0 {
		t.Fatal("retention left task/notice", found, err)
	}
	result, err = s.Create(context.Background(), "Independent user task", &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "UTC"}, "", false)
	if err != nil || result.ScheduleError != nil {
		t.Fatal(result, err)
	}
	if _, err := deleter.DeleteAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err := repo.ListTasks(context.Background(), tasks.PageRequest{Limit: 32})
	if err != nil || len(page.Items) != 0 || len(port.pending) != 0 || len(port.delivered) != 0 {
		t.Fatal("delete-all retained independent task or OS notification", page, err)
	}
}

func TestRelayedCutoffPreservesNewerCaptureAndItsNativeNotice(t *testing.T) {
	ctx := context.Background()
	repo := store(t)
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	port := &fakeNotifications{auth: reminders.Allowed}
	s, _ := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	defer s.Close()
	deleter, _ := deletion.NewService(repo, nil)
	deleter.SetNotificationCleanup(s)
	o, _ := capture.SanitizeObservation("sh.blur.notes", "local", "A genuinely newer reminder source", 512)
	if err := repo.PutObservationAt(ctx, o, now); err != nil {
		t.Fatal(err)
	}
	result, err := s.Create(ctx, "Review later capture", &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "UTC"}, o.ContentID(), false)
	if err != nil || result.ScheduleError != nil {
		t.Fatal(result, err)
	}
	// A preserved source must not even attempt native cancellation.
	port.removeError = reminders.ErrUnavailable
	if _, err := deleter.DeleteSourceThrough(ctx, o.ContentID(), now.Add(-time.Second)); err != nil {
		t.Fatal("older cutoff attempted notification cancellation", err)
	}
	if len(port.pending) != 1 {
		t.Fatal("older cutoff removed newer notice")
	}
	if _, err := deleter.DeleteSourceThrough(ctx, o.ContentID(), now); !errors.Is(err, reminders.ErrUnavailable) {
		t.Fatal("covered source ignored native cancellation failure", err)
	}
	if _, found, err := repo.GetObservation(ctx, o.ContentID()); err != nil || !found {
		t.Fatal("cancellation failure erased source", found, err)
	}
	port.removeError = nil
	if _, err := deleter.DeleteSourceThrough(ctx, o.ContentID(), now); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repo.GetTask(ctx, result.Item.ID); err != nil || found || len(port.pending) != 0 {
		t.Fatal("covered cutoff retained task or native notice", found, err)
	}
}

func TestConditionalDeletionWithoutTasksDoesNotRequireNativeNotifications(t *testing.T) {
	ctx := context.Background()
	repo := store(t)
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	port := &fakeNotifications{auth: reminders.Allowed, snapshotError: reminders.ErrUnavailable}
	s, _ := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	defer s.Close()
	deleter, _ := deletion.NewService(repo, nil)
	deleter.SetNotificationCleanup(s)
	o, _ := capture.SanitizeObservation("reader.app", "example.com", "Ordinary source with no linked tasks", 512)
	if err := repo.PutObservationAt(ctx, o, now); err != nil {
		t.Fatal(err)
	}
	if _, err := deleter.DeleteSourceThrough(ctx, o.ContentID(), now); err != nil {
		t.Fatal("unrelated native failure prevented ordinary source deletion", err)
	}
	if _, found, err := repo.GetObservation(ctx, o.ContentID()); err != nil || found {
		t.Fatal("ordinary source survived", found, err)
	}
}

func TestRetryPersistsActualQueueOutcomeImmediately(t *testing.T) {
	repo := store(t)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	port := &fakeNotifications{auth: reminders.Allowed, queueError: reminders.ErrUnavailable}
	s, _ := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	defer s.Close()
	result, err := s.Create(context.Background(), "Retry a native queue failure", &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "UTC"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(context.Background()); !errors.Is(err, reminders.ErrUnavailable) {
		t.Fatal(err)
	}
	item, _, err := repo.GetTask(context.Background(), result.Item.ID)
	if err != nil || item.Delivery.State != tasks.Failed || item.Delivery.Attempted {
		t.Fatalf("definite queue failure left uncertain receipt: %+v %v", item.Delivery, err)
	}
	port.queueError = nil
	runPass(t, s)
	delete(port.pending, reminders.NotificationID(result.Item)) // OS removed a future pending request
	runPass(t, s)
	item, _, err = repo.GetTask(context.Background(), result.Item.ID)
	if err != nil || item.Delivery.State != tasks.Scheduled || !item.Delivery.Attempted {
		t.Fatalf("successful queue repair did not persist acknowledgment: %+v %v", item.Delivery, err)
	}
}

func TestLegacyReminderTextUpgradePreservesIDRetriesAndDoesNotRepeat(t *testing.T) {
	repo := store(t)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	port := &fakeNotifications{auth: reminders.Allowed, versionAware: true}
	s, _ := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	result, err := s.Create(context.Background(), "Get eggs", &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "UTC"}, "", false)
	if err != nil || result.ScheduleError != nil {
		t.Fatal(result, err)
	}
	id := reminders.NotificationID(result.Item)
	legacy := port.pending[id]
	legacy.Title = ""
	port.pending[id] = legacy
	port.queueError = reminders.ErrUnavailable
	if err := s.Reconcile(context.Background()); !errors.Is(err, reminders.ErrUnavailable) || port.pending[id].Title != "" {
		t.Fatal("failed migration falsely replaced legacy payload", err)
	}
	port.queueError = nil
	runPass(t, s)
	if port.pending[id].Title != result.Item.Title || len(port.pending) != 1 || !port.pending[id].Schedule.DueAt.Equal(result.Item.Reminder.DueAt) {
		t.Fatal("legacy upgrade lost task text, changed ID or changed due time")
	}
	queued := port.queues
	runPass(t, s)
	s.Close()
	s, _ = reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	defer s.Close()
	runPass(t, s)
	if port.queues != queued {
		t.Fatal("content-aware future reminder was rescheduled every pass/restart")
	}
	legacy = port.pending[id]
	legacy.Title = ""
	port.pending[id] = legacy
	port.versionAware = false
	runPass(t, s)
	if port.queues != queued {
		t.Fatal("unknown snapshot version caused indiscriminate replacement")
	}
	port.versionAware = true
	now = now.Add(2 * time.Hour)
	runPass(t, s)
	if port.queues != queued {
		t.Fatal("legacy elapsed pending reminder was re-alerted")
	}
	delete(port.pending, id)
	port.delivered = map[string]bool{id: true}
	runPass(t, s)
	if port.queues != queued {
		t.Fatal("legacy delivered reminder was re-alerted")
	}
}

func TestFailedLegacyTextUpgradeCannotRealertAfterDismissal(t *testing.T) {
	repo := store(t)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	port := &fakeNotifications{auth: reminders.Allowed, versionAware: true}
	s, _ := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	defer s.Close()
	result, err := s.Create(context.Background(), "Get eggs", &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "UTC"}, "", false)
	if err != nil || result.ScheduleError != nil {
		t.Fatal(result, err)
	}
	id := reminders.NotificationID(result.Item)
	legacy := port.pending[id]
	legacy.Title = ""
	port.pending[id] = legacy
	port.queueError = reminders.ErrUnavailable
	if err := s.Reconcile(context.Background()); !errors.Is(err, reminders.ErrUnavailable) {
		t.Fatal("synthetic replacement failure not observed", err)
	}
	// The still-existing old notice subsequently fires and is dismissed before
	// the next snapshot. Its failed replacement must retain the prior attempt.
	delete(port.pending, id)
	queued := port.queues
	port.queueError = nil
	now = now.Add(2 * time.Hour)
	runPass(t, s)
	if port.queues != queued {
		t.Fatal("failed legacy replacement forgot the prior notice and alerted again")
	}
}

func TestOverdueUnsentIntentCatchesUpOnceAndVeryOldIntentStaysVisible(t *testing.T) {
	repo := store(t)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	port := &fakeNotifications{auth: reminders.Allowed}
	item := tasks.Item{ID: "1234567890abcdef1234567890abcdef", Revision: 1, Title: "Unsent intent after interrupted startup", State: tasks.Open, CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-3 * time.Hour), UserAuthored: true, Reminder: &tasks.Reminder{DueAt: now.Add(-time.Hour), Timezone: "UTC"}, Delivery: tasks.Delivery{State: tasks.NeedsSync, At: now.Add(-3 * time.Hour)}}
	if err := repo.PutTask(context.Background(), item, 0); err != nil {
		t.Fatal(err)
	}
	s, _ := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	defer s.Close()
	runPass(t, s)
	if port.queues != 1 || !port.pending[reminders.NotificationID(item)].Schedule.DueAt.After(now) {
		t.Fatal("unsent overdue intent did not catch up once")
	}
	recovered, _, _ := repo.GetTask(context.Background(), item.ID)
	if !recovered.Delivery.NextAt.After(now) || !recovered.Reminder.DueAt.Before(now) {
		t.Fatal("catch-up time replaced intent or was not persisted", recovered)
	}
	runPass(t, s)
	if port.queues != 1 {
		t.Fatal("overdue intent queued twice")
	}
	old := tasks.Clone(item)
	old.ID = "abcdef1234567890abcdef1234567890"
	old.CreatedAt = now.Add(-72 * time.Hour)
	old.UpdatedAt = old.CreatedAt
	old.Reminder.DueAt = now.Add(-48 * time.Hour)
	old.Delivery.At = old.CreatedAt
	if err := repo.PutTask(context.Background(), old, 0); err != nil {
		t.Fatal(err)
	}
	s.Wake()
	runPass(t, s)
	got, found, err := repo.GetTask(context.Background(), old.ID)
	if err != nil || !found || got.State != tasks.Open || got.Delivery.State != tasks.Elapsed || port.queues != 1 {
		t.Fatal("old intent was lost or unexpectedly alerted", got, err)
	}
}

func TestPastTitleEditCannotRealertAndRecurringRecoveryUsesNextCalendarOccurrence(t *testing.T) {
	repo := store(t)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	port := &fakeNotifications{auth: reminders.Allowed}
	s, _ := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	defer s.Close()
	result, err := s.Create(context.Background(), "One-time reminder", &tasks.Reminder{DueAt: now.Add(time.Minute), Timezone: "UTC"}, "", false)
	if err != nil || result.ScheduleError != nil {
		t.Fatal(result, err)
	}
	delete(port.pending, reminders.NotificationID(result.Item))
	now = now.Add(2 * time.Minute)
	runPass(t, s)
	item, _, _ := repo.GetTask(context.Background(), result.Item.ID)
	item.Title = "Corrected reminder title"
	if _, err := s.Update(context.Background(), item, false); err != nil {
		t.Fatal(err)
	}
	if port.queues != 1 {
		t.Fatal("title edit re-alerted a past reminder")
	}
	draft, err := reminders.Parse("remind me every day at 09:00 to check backups", now, "Europe/Brussels")
	if err != nil || draft.Reminder == nil {
		t.Fatal(draft, err)
	}
	repeat, err := s.Create(context.Background(), draft.Title, draft.Reminder, "", false)
	if err != nil || repeat.ScheduleError != nil {
		t.Fatal(repeat, err)
	}
	delete(port.pending, reminders.NotificationID(repeat.Item))
	now = now.AddDate(0, 0, 4)
	s.Wake()
	runPass(t, s)
	notice, found := port.pending[reminders.NotificationID(repeat.Item)]
	if !found || !notice.Schedule.DueAt.After(now) || notice.Schedule.Repeat != tasks.Daily {
		t.Fatal("repeat recovery replayed an old occurrence", notice)
	}
}

func TestSubmissionRetryUsesOneDurableTaskAndDoesNotRealertAfterDelivery(t *testing.T) {
	repo := store(t)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	port := &fakeNotifications{auth: reminders.Allowed}
	s, _ := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	defer s.Close()
	id := "1234567890abcdef1234567890abcdef"
	schedule := &tasks.Reminder{DueAt: now.Add(time.Minute), Timezone: "UTC"}
	first, err := s.Create(context.Background(), "One user submission", schedule, "", false, id)
	if err != nil || first.ScheduleError != nil {
		t.Fatal(first, err)
	}
	retry, err := s.Create(context.Background(), "One user submission", schedule, "", false, id)
	if err != nil || retry.ScheduleError != nil || retry.Item.ID != first.Item.ID || port.queues != 1 {
		t.Fatal("retry duplicated creation/schedule", retry, err)
	}
	if _, err := s.Create(context.Background(), "Different request with reused ID", schedule, "", false, id); !errors.Is(err, tasks.ErrConflict) {
		t.Fatal("request ID accepted a different payload", err)
	}
	delete(port.pending, reminders.NotificationID(first.Item))
	port.delivered = map[string]bool{reminders.NotificationID(first.Item): true}
	now = now.Add(2 * time.Minute)
	retry, err = s.Create(context.Background(), "One user submission", schedule, "", false, id)
	if err != nil || retry.ScheduleError != nil || retry.Item.Delivery.State != tasks.Delivered || port.queues != 1 {
		t.Fatal("late submission retry re-alerted", retry, err)
	}
	page, err := repo.ListTasks(context.Background(), tasks.PageRequest{Limit: 32})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("idempotent request created multiple tasks", page, err)
	}
}
