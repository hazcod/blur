package reminders

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/tasks"
)

type Options struct {
	Now      func() time.Time
	OnChange func()
}
type Result struct {
	Item          tasks.Item
	ScheduleError error
}
type Status struct {
	State         string
	Authorization Authorization
	ErrorCode     string
}

type Service struct {
	repository      tasks.Repository
	port            NotificationPort
	options         Options
	runMu           sync.Mutex
	mu              sync.Mutex
	ctx             context.Context
	cancel          context.CancelFunc
	runCancel       context.CancelFunc
	started, closed bool
	wake            chan struct{}
	done, stopped   chan struct{}
	after           string
	dirty           bool
	status          Status
}

func NewService(repository tasks.Repository, port NotificationPort, options Options) (*Service, error) {
	if repository == nil || !repository.EncryptedAtRest() || port == nil {
		return nil, tasks.ErrInvalid
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{repository: repository, port: port, options: options, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), stopped: make(chan struct{}), status: Status{State: "loading"}}, nil
}
func (s *Service) changed() {
	if s.options.OnChange != nil {
		s.options.OnChange()
	}
}
func (s *Service) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) Wake() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
	s.notify()
}
func (s *Service) Start() {
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	go s.run()
	s.notify()
}
func (s *Service) run() {
	defer close(s.done)
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		case <-timer.C:
		}
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		err := s.Reconcile(ctx)
		cancel()
		delay := time.Minute
		if errors.Is(err, ErrMoreWork) {
			delay = time.Second
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
	}
}
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.stopped
		return
	}
	s.closed = true
	s.cancel()
	if s.runCancel != nil {
		s.runCancel()
	}
	started := s.started
	s.mu.Unlock()
	if started {
		<-s.done
	}
	s.runMu.Lock()
	s.runMu.Unlock() //nolint:staticcheck // Wait for an in-flight run to release the mutex before Close returns.
	close(s.stopped)
}
func (s *Service) Status() Status {
	if s == nil {
		return Status{State: "unavailable", Authorization: Unsupported}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Service) RequestAuthorization(parent context.Context) (Authorization, error) {
	ctx, done, err := s.enter(parent)
	if err != nil {
		return "", err
	}
	auth, err := s.port.RequestAuthorization(ctx)
	if err == nil {
		s.mu.Lock()
		s.status.Authorization = auth
		s.mu.Unlock()
	}
	done()
	s.Wake()
	s.changed()
	return auth, err
}
func (s *Service) enter(parent context.Context) (context.Context, func(), error) {
	if s == nil || parent == nil {
		return nil, nil, tasks.ErrInvalid
	}
	s.runMu.Lock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.runMu.Unlock()
		return nil, nil, ErrUnavailable
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(s.ctx, cancel)
	s.runCancel = cancel
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		stop()
		s.mu.Lock()
		s.runCancel = nil
		s.mu.Unlock()
		s.runMu.Unlock()
	}, nil
}
func (s *Service) now() time.Time { return s.options.Now().UTC() }

func (s *Service) Create(parent context.Context, title string, reminder *tasks.Reminder, sourceID string, authorize bool, requestIDs ...string) (Result, error) {
	ctx, done, err := s.enter(parent)
	if err != nil {
		return Result{}, err
	}
	defer done()
	defer s.changed()
	defer s.Wake()
	now := s.now()
	if !tasks.ValidTime(now) || !tasks.ValidTitle(title) || sourceID != "" && !tasks.ValidSourceID(sourceID) || reminder != nil && reminder.Validate() != nil || len(requestIDs) > 1 || len(requestIDs) == 1 && !tasks.ValidID(requestIDs[0]) {
		return Result{}, tasks.ErrInvalid
	}
	requestReminder := reminder
	if reminder != nil {
		copy := *reminder
		copy.DueAt = copy.DueAt.UTC()
		requestReminder = &copy
	}
	body, _ := json.Marshal(struct {
		Title    string
		Reminder *tasks.Reminder
		SourceID string
	}{title, requestReminder, sourceID})
	hash := sha256.Sum256(body)
	clear(body)
	requestHash := hex.EncodeToString(hash[:])
	id := ""
	if len(requestIDs) == 1 {
		id = requestIDs[0]
		existing, found, err := s.repository.GetTask(ctx, id)
		if err != nil {
			return Result{}, err
		}
		if found {
			if existing.RequestHash != requestHash {
				return Result{}, tasks.ErrConflict
			}
			return s.synchronizeResult(ctx, existing, authorize), nil
		}
	}
	if reminder != nil && !reminder.DueAt.After(now) {
		return Result{}, tasks.ErrInvalid
	}
	var nonce [16]byte
	if id == "" {
		if _, err := rand.Read(nonce[:]); err != nil {
			return Result{}, ErrUnavailable
		}
		id = hex.EncodeToString(nonce[:])
	}
	item := tasks.Item{ID: id, RequestHash: requestHash, Revision: 1, Title: title, State: tasks.Open, CreatedAt: now, UpdatedAt: now, SourceID: sourceID, UserAuthored: sourceID == "", Reminder: reminder}
	tasks.RecordTransition(&item, "")
	if reminder != nil {
		item.Delivery = tasks.Delivery{State: tasks.NeedsSync, At: now}
	}
	if err := s.repository.PutTask(ctx, item, 0); err != nil {
		return Result{}, err
	}
	return s.synchronizeResult(ctx, item, authorize), nil
}

func sameReminder(a, b *tasks.Reminder) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.DueAt.Equal(b.DueAt) && a.Timezone == b.Timezone && a.Repeat == b.Repeat && a.Hour == b.Hour && a.Minute == b.Minute && a.Weekday == b.Weekday
}

// Update consumes the currently displayed revision. A changed task never gets
// overwritten by an older UI, and title edits cannot re-alert a past reminder.
func (s *Service) Update(parent context.Context, edited tasks.Item, authorize bool) (Result, error) {
	ctx, done, err := s.enter(parent)
	if err != nil {
		return Result{}, err
	}
	defer done()
	defer s.changed()
	defer s.Wake()
	old, found, err := s.repository.GetTask(ctx, edited.ID)
	if err != nil {
		return Result{}, err
	}
	if !found || old.Revision != edited.Revision {
		return Result{}, tasks.ErrConflict
	}
	if !tasks.ValidTitle(edited.Title) || edited.State != tasks.Open && edited.State != tasks.Completed && edited.State != tasks.Cancelled {
		return Result{}, tasks.ErrInvalid
	}
	now := s.now()
	if now.Before(old.UpdatedAt) {
		now = old.UpdatedAt
	}
	changed := !sameReminder(old.Reminder, edited.Reminder) || old.State != tasks.Open && edited.State == tasks.Open
	if edited.Reminder != nil && (edited.Reminder.Validate() != nil || edited.State == tasks.Open && changed && !edited.Reminder.DueAt.After(now)) {
		return Result{}, tasks.ErrInvalid
	}
	item := tasks.Clone(old)
	item.Title, item.State, item.Reminder = edited.Title, edited.State, edited.Reminder
	item.Revision++
	item.UpdatedAt = now
	tasks.RecordTransition(&item, old.State)
	if item.Reminder != nil && item.State != tasks.Open {
		item.Delivery = tasks.Delivery{State: tasks.Elapsed, At: now, Attempted: wasAttempted(old)}
	}
	if item.Reminder == nil {
		item.Delivery = tasks.Delivery{}
	} else if changed {
		item.Delivery = tasks.Delivery{State: tasks.NeedsSync, At: now}
	}
	if err := s.removeItemNotices(ctx, old); err != nil {
		return Result{}, err
	}
	if err := s.repository.PutTask(ctx, item, old.Revision); err != nil {
		return Result{}, err
	}
	return s.synchronizeResult(ctx, item, authorize), nil
}

func (s *Service) synchronizeResult(ctx context.Context, item tasks.Item, authorize bool) Result {
	if item.State != tasks.Open || item.Reminder == nil {
		return Result{Item: item}
	}
	auth, err := s.port.Authorization(ctx)
	if err == nil && authorize && auth == NotDetermined {
		auth, err = s.port.RequestAuthorization(ctx)
	}
	if err == nil {
		s.mu.Lock()
		s.status.Authorization = auth
		s.mu.Unlock()
	}
	var snapshot Snapshot
	if err == nil {
		snapshot, err = s.port.Snapshot(ctx)
	}
	if err == nil {
		err = s.reconcileItem(ctx, item, auth, snapshot)
	}
	updated, found, readErr := s.repository.GetTask(ctx, item.ID)
	if readErr == nil && found {
		item = updated
	}
	if err == nil && readErr != nil {
		err = readErr
	}
	return Result{Item: item, ScheduleError: err}
}

func (s *Service) setDelivery(ctx context.Context, item tasks.Item, state tasks.DeliveryState, attempted bool) error {
	next := item.Delivery.NextAt
	if state != tasks.Scheduling && state != tasks.Scheduled {
		next = time.Time{}
	}
	return s.setDeliveryAt(ctx, item, state, attempted, next)
}
func (s *Service) setDeliveryAt(ctx context.Context, item tasks.Item, state tasks.DeliveryState, attempted bool, next time.Time) error {
	if item.Delivery.State == state && item.Delivery.Attempted == attempted && item.Delivery.NextAt.Equal(next) {
		return nil
	}
	return s.repository.SetTaskDelivery(ctx, item.ID, item.Revision, tasks.Delivery{State: state, At: s.now(), Attempted: attempted, NextAt: next})
}
func wasAttempted(item tasks.Item) bool {
	return item.Delivery.Attempted || item.Delivery.State == tasks.Scheduling || item.Delivery.State == tasks.Scheduled || item.Delivery.State == tasks.Delivered
}

func (s *Service) reconcileItem(ctx context.Context, item tasks.Item, auth Authorization, snapshot Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if item.State != tasks.Open || item.Reminder == nil {
		return nil
	}
	id := NotificationID(item)
	attempted := wasAttempted(item)
	if auth != Allowed {
		if err := s.setDelivery(ctx, item, tasks.Blocked, attempted); err != nil {
			return err
		}
		if auth == Unsupported {
			return ErrUnsupported
		}
		return ErrPermission
	}
	if slices.Contains(snapshot.Pending, id) {
		next := snapshot.NextAt[id]
		if next.IsZero() {
			next = item.Delivery.NextAt
			if item.Reminder.Repeat != tasks.Once {
				if at, err := tasks.NextOccurrence(*item.Reminder, s.now()); err == nil {
					next = at
				}
			} else if next.IsZero() {
				next = item.Reminder.DueAt
			}
		}
		if version, known := snapshot.ContentVersions[id]; known && version < NotificationContentVersion && next.After(s.now()) {
			// Replace a known legacy future payload under the same OS identifier.
			// Unknown ports and elapsed/delivered alerts never trigger migration.
			schedule := *item.Reminder
			schedule.DueAt = next
			return s.queueItem(ctx, item, id, schedule, s.now(), true)
		}
		return s.setDeliveryAt(ctx, item, tasks.Scheduled, true, next)
	}
	if slices.Contains(snapshot.Delivered, id) && item.Reminder.Repeat == tasks.Once {
		return s.setDelivery(ctx, item, tasks.Delivered, true)
	}
	now := s.now()
	r := *item.Reminder
	if r.Repeat == tasks.Once && !r.DueAt.After(now) {
		if item.Delivery.State == tasks.Delivered {
			return nil
		}
		// A dismissed notification is no longer observable. Re-alerting merely
		// because it disappeared would duplicate notifications after every restart.
		if attempted || now.Sub(r.DueAt) > 24*time.Hour {
			return s.setDelivery(ctx, item, tasks.Elapsed, attempted)
		}
		r.DueAt = now.Add(time.Second)
	} else if r.Repeat != tasks.Once {
		next, err := tasks.NextOccurrence(r, now)
		if err != nil {
			return err
		}
		r.DueAt = next
	}
	return s.queueItem(ctx, item, id, r, now, false)
}

func (s *Service) queueItem(ctx context.Context, item tasks.Item, id string, r tasks.Reminder, now time.Time, replacingPending bool) error {
	r.DueAt = ceilReminderSecond(r.DueAt)
	if err := s.setDeliveryAt(ctx, item, tasks.Scheduling, true, r.DueAt); err != nil {
		return err
	}
	item.Delivery = tasks.Delivery{State: tasks.Scheduling, At: now, Attempted: true, NextAt: r.DueAt}
	err := s.port.Queue(ctx, Notice{ID: id, Title: item.Title, Schedule: r})
	if err != nil {
		if errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		// A failed content upgrade does not cancel the old pending notice. Keep
		// that prior attempt recorded so its later dismissal cannot re-alert.
		if writeErr := s.setDelivery(ctx, item, tasks.Failed, replacingPending); writeErr != nil {
			return writeErr
		}
		return err
	}
	return s.setDeliveryAt(ctx, item, tasks.Scheduled, true, r.DueAt)
}

func ceilReminderSecond(at time.Time) time.Time {
	rounded := at.UTC().Truncate(time.Second)
	if rounded.Before(at) {
		rounded = rounded.Add(time.Second)
	}
	return rounded
}

func validSnapshot(snapshot Snapshot) bool {
	if len(snapshot.Pending)+len(snapshot.Delivered) > MaxSnapshotIDs || len(snapshot.ContentVersions) > MaxSnapshotIDs {
		return false
	}
	for id, version := range snapshot.ContentVersions {
		if !slices.Contains(snapshot.Pending, id) || version > NotificationContentVersion {
			return false
		}
	}
	for id, at := range snapshot.NextAt {
		if !slices.Contains(snapshot.Pending, id) || !tasks.ValidTime(at) {
			return false
		}
	}
	for _, ids := range [][]string{snapshot.Pending, snapshot.Delivered} {
		seen := map[string]bool{}
		for _, id := range ids {
			if _, _, ok := NotificationTaskID(id); !ok || seen[id] {
				return false
			}
			seen[id] = true
		}
	}
	return true
}

func (s *Service) Reconcile(parent context.Context) (result error) {
	ctx, done, err := s.enter(parent)
	if err != nil {
		return err
	}
	defer done()
	defer s.changed()
	auth, err := s.port.Authorization(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.status = Status{State: "loading", Authorization: auth}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if result == nil {
			s.status.State = "ready"
		} else if errors.Is(result, ErrMoreWork) {
			s.status.State = "loading"
		} else {
			s.status.State = "unavailable"
			s.status.ErrorCode = "delivery_unavailable"
		}
		if auth == Denied || auth == NotDetermined || auth == Unsupported {
			s.status.State = "blocked"
		}
	}()
	snapshot, err := s.port.Snapshot(ctx)
	if err != nil {
		return err
	}
	if !validSnapshot(snapshot) {
		return ErrUnavailable
	}
	// Remove native requests left by an interrupted edit or deletion. Native
	// identifiers alone are sufficient; no OS notification text is read.
	var stale []string
	for _, id := range append(slices.Clone(snapshot.Pending), snapshot.Delivered...) {
		taskID, revision, _ := NotificationTaskID(id)
		item, found, err := s.repository.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if !found || item.Revision != revision || item.State != tasks.Open || item.Reminder == nil {
			stale = append(stale, id)
		}
	}
	if len(stale) > 0 {
		if err := s.port.Remove(ctx, stale); err != nil {
			return err
		}
	}
	for count := 0; count < 2; count++ {
		page, err := s.repository.ListTasks(ctx, tasks.PageRequest{Limit: tasks.MaxPageSize, After: s.after})
		if err != nil {
			return err
		}
		for _, item := range page.Items {
			err := s.reconcileItem(ctx, item, auth, snapshot)
			if err != nil && !errors.Is(err, ErrPermission) && !errors.Is(err, ErrUnsupported) {
				return err
			}
		}
		if !page.HasMore {
			s.after = ""
			s.mu.Lock()
			dirty := s.dirty
			s.dirty = false
			s.mu.Unlock()
			if dirty {
				return ErrMoreWork
			}
			return nil
		}
		if page.Next == "" || page.Next == s.after {
			return tasks.ErrInvalid
		}
		s.after = page.Next
	}
	return ErrMoreWork
}

func (s *Service) removeItemNotices(ctx context.Context, item tasks.Item) error {
	return s.removeItemsNotices(ctx, []tasks.Item{item})
}
func (s *Service) removeItemsNotices(ctx context.Context, items []tasks.Item) error {
	snapshot, err := s.port.Snapshot(ctx)
	if err != nil {
		return err
	}
	if !validSnapshot(snapshot) {
		return ErrUnavailable
	}
	ids := []string{}
	roots := map[string]bool{}
	for _, item := range items {
		roots[item.ID] = true
		ids = append(ids, NotificationID(item))
	}
	for _, id := range append(slices.Clone(snapshot.Pending), snapshot.Delivered...) {
		taskID, _, _ := NotificationTaskID(id)
		if roots[taskID] && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	for start := 0; start < len(ids); start += 256 {
		if err := s.port.Remove(ctx, ids[start:min(start+256, len(ids))]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Delete(parent context.Context, id string, revision uint64) error {
	ctx, done, err := s.enter(parent)
	if err != nil {
		return err
	}
	defer done()
	defer s.changed()
	item, found, err := s.repository.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if !found || item.Revision != revision {
		return tasks.ErrConflict
	}
	if err := s.removeItemNotices(ctx, item); err != nil {
		return err
	}
	return s.repository.DeleteTask(ctx, id, revision)
}

// GuardSources keeps scheduling paused across source deletion. If cancellation
// fails, deletion is rejected instead of leaving an orphan scheduled reminder.
func (s *Service) WithSourceDeletionGuard(parent context.Context, action func(context.Context, func([]tasks.Item) error) error) error {
	if action == nil {
		return tasks.ErrInvalid
	}
	ctx, done, err := s.enter(parent)
	if err != nil {
		return err
	}
	defer func() { done(); s.Wake() }()
	return action(ctx, func(items []tasks.Item) error {
		if len(items) == 0 {
			return nil
		}
		return s.removeItemsNotices(ctx, items)
	})
}

func (s *Service) GuardSources(parent context.Context, sourceIDs []string) (func(), error) {
	ctx, done, err := s.enter(parent)
	if err != nil {
		return nil, err
	}
	var linked []tasks.Item
	for start := 0; start < len(sourceIDs); start += 8 {
		items, err := s.repository.TasksForSources(ctx, sourceIDs[start:min(start+8, len(sourceIDs))])
		if err != nil {
			done()
			return nil, err
		}
		linked = append(linked, items...)
	}
	if len(linked) > 0 {
		if err := s.removeItemsNotices(ctx, linked); err != nil {
			done()
			return nil, err
		}
	}
	var once sync.Once
	return func() { once.Do(func() { done(); s.Wake() }) }, nil
}
func (s *Service) GuardAll(parent context.Context) (func(), error) {
	ctx, done, err := s.enter(parent)
	if err != nil {
		return nil, err
	}
	if err := s.port.RemoveAll(ctx); err != nil {
		done()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { done(); s.Wake() }) }, nil
}
