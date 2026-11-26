//go:build linux

package linux

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/tasks"
)

// The freedesktop notification service shows notices immediately and has no
// scheduling, so Notifications keeps the schedule in this process and shows
// each reminder when it is due. Pending reminders exist only while Blur runs:
// after a restart the reminder service queues future reminders again and marks
// one-time reminders that came due while Blur was closed as elapsed.
type Notifications struct {
	backend notificationBackend
	now     func() time.Time
	// maxWait bounds each sleep so a suspended machine notices due reminders
	// soon after resume: Go timers follow the monotonic clock, which pauses
	// during suspend, while schedules are wall-clock times.
	maxWait time.Duration

	mu        sync.Mutex
	pending   map[string]*scheduledNotice
	delivered []string
	shown     map[uint32]string

	wake      chan struct{}
	opens     chan string
	done      chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

type scheduledNotice struct {
	title    string
	schedule tasks.Reminder
	next     time.Time
}

// notificationBackend is the desktop notification service. show returns the
// service's notification ID; events reports activated or closed notices.
type notificationBackend interface {
	available(context.Context) bool
	show(ctx context.Context, summary, body string) (uint32, error)
	close(id uint32)
	events() <-chan notificationEvent
}

type notificationEvent struct {
	id        uint32
	activated bool
}

const maxDeliveredNotices = reminders.MaxSnapshotIDs / 4

var _ reminders.NotificationPort = (*Notifications)(nil)

// NewNotifications returns a reminder port backed by the session bus
// notification service (GNOME Shell, KDE Plasma, dunst, mako and others).
func NewNotifications() *Notifications {
	return newNotifications(newDBusNotifications(), time.Now, 30*time.Second)
}

func newNotifications(backend notificationBackend, now func() time.Time, maxWait time.Duration) *Notifications {
	return &Notifications{
		backend: backend,
		now:     now,
		maxWait: maxWait,
		pending: map[string]*scheduledNotice{},
		shown:   map[uint32]string{},
		wake:    make(chan struct{}, 1),
		opens:   make(chan string, 4),
		done:    make(chan struct{}),
	}
}

// Opened delivers the task ID of a reminder the person clicked.
func (n *Notifications) Opened() <-chan string { return n.opens }

// Start runs the scheduler until Close.
func (n *Notifications) Start() {
	n.startOnce.Do(func() {
		go n.run()
		go n.watchEvents()
	})
}

func (n *Notifications) Close() {
	n.closeOnce.Do(func() { close(n.done) })
}

// Authorization reports whether a notification service is reachable. Linux has
// no per-app notification permission prompt.
func (n *Notifications) Authorization(ctx context.Context) (reminders.Authorization, error) {
	if n.backend.available(ctx) {
		return reminders.Allowed, nil
	}
	return reminders.Unsupported, nil
}

func (n *Notifications) RequestAuthorization(ctx context.Context) (reminders.Authorization, error) {
	return n.Authorization(ctx)
}

func (n *Notifications) Snapshot(ctx context.Context) (reminders.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return reminders.Snapshot{}, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	snapshot := reminders.Snapshot{
		Pending:         make([]string, 0, len(n.pending)),
		Delivered:       slices.Clone(n.delivered),
		NextAt:          make(map[string]time.Time, len(n.pending)),
		ContentVersions: make(map[string]uint8, len(n.pending)),
	}
	for id, notice := range n.pending {
		snapshot.Pending = append(snapshot.Pending, id)
		snapshot.NextAt[id] = notice.next
		snapshot.ContentVersions[id] = reminders.NotificationContentVersion
	}
	slices.Sort(snapshot.Pending)
	return snapshot, nil
}

// Queue schedules or replaces the reminder with notice.ID. Like macOS, a
// one-time reminder must be in the future.
func (n *Notifications) Queue(ctx context.Context, notice reminders.Notice) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, _, ok := reminders.NotificationTaskID(notice.ID); !ok || notice.Schedule.Validate() != nil || !tasks.ValidTitle(notice.Title) {
		return tasks.ErrInvalid
	}
	now := n.now()
	next, err := firstOccurrence(notice.Schedule, now)
	if err != nil {
		return err
	}
	n.mu.Lock()
	if _, replacing := n.pending[notice.ID]; !replacing && len(n.pending) >= reminders.MaxSnapshotIDs/2 {
		n.mu.Unlock()
		return tasks.ErrCapacity
	}
	n.pending[notice.ID] = &scheduledNotice{title: notice.Title, schedule: notice.Schedule, next: next}
	n.mu.Unlock()
	n.Start()
	n.poke()
	return nil
}

func firstOccurrence(r tasks.Reminder, now time.Time) (time.Time, error) {
	if r.Repeat == tasks.Once {
		at := r.DueAt.UTC().Truncate(time.Second)
		if at.Before(r.DueAt) {
			at = at.Add(time.Second)
		}
		if !at.After(now) {
			return time.Time{}, tasks.ErrInvalid
		}
		return at, nil
	}
	if r.DueAt.After(now) {
		return r.DueAt.UTC(), nil
	}
	next, err := tasks.NextOccurrence(r, now)
	if err != nil {
		return time.Time{}, tasks.ErrInvalid
	}
	return next, nil
}

func (n *Notifications) Remove(ctx context.Context, ids []string) error {
	if len(ids) > reminders.MaxSnapshotIDs {
		return tasks.ErrCapacity
	}
	for _, id := range ids {
		if _, _, ok := reminders.NotificationTaskID(id); !ok {
			return tasks.ErrInvalid
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	n.remove(func(id string) bool { return slices.Contains(ids, id) })
	return nil
}

func (n *Notifications) RemoveAll(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.remove(func(string) bool { return true })
	return nil
}

func (n *Notifications) remove(match func(string) bool) {
	n.mu.Lock()
	for id := range n.pending {
		if match(id) {
			delete(n.pending, id)
		}
	}
	n.delivered = slices.DeleteFunc(n.delivered, match)
	var visible []uint32
	for native, id := range n.shown {
		if match(id) {
			visible = append(visible, native)
			delete(n.shown, native)
		}
	}
	n.mu.Unlock()
	for _, native := range visible {
		n.backend.close(native)
	}
	n.poke()
}

func (n *Notifications) poke() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

func (n *Notifications) run() {
	for {
		timer := time.NewTimer(n.untilNextDue())
		select {
		case <-n.done:
			timer.Stop()
			return
		case <-n.wake:
			timer.Stop()
		case <-timer.C:
		}
		n.deliverDue()
	}
}

func (n *Notifications) untilNextDue() time.Duration {
	n.mu.Lock()
	defer n.mu.Unlock()
	wait := n.maxWait
	now := n.now()
	for _, notice := range n.pending {
		if until := notice.next.Sub(now); until < wait {
			wait = max(until, 0)
		}
	}
	return wait
}

type dueNotice struct {
	id, title string
}

// deliverDue shows every reminder whose time has passed. A repeating reminder
// moves to its next occurrence; a one-time reminder becomes delivered.
func (n *Notifications) deliverDue() {
	now := n.now()
	var due []dueNotice
	n.mu.Lock()
	for id, notice := range n.pending {
		if notice.next.After(now) {
			continue
		}
		due = append(due, dueNotice{id: id, title: notice.title})
		if notice.schedule.Repeat == tasks.Once {
			delete(n.pending, id)
		} else if next, err := tasks.NextOccurrence(notice.schedule, now); err == nil {
			notice.next = next
		} else {
			delete(n.pending, id)
		}
		n.delivered = append(slices.DeleteFunc(n.delivered, func(existing string) bool { return existing == id }), id)
	}
	if extra := len(n.delivered) - maxDeliveredNotices; extra > 0 {
		n.delivered = slices.Delete(n.delivered, 0, extra)
	}
	n.mu.Unlock()
	slices.SortFunc(due, func(a, b dueNotice) int { return strings.Compare(a.id, b.id) })
	var removed []uint32
	for _, notice := range due {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		native, err := n.backend.show(ctx, "Reminder", notice.title)
		cancel()
		if err != nil {
			// Never report an unshown reminder as delivered; the reminder
			// service then records a one-time reminder as elapsed.
			n.mu.Lock()
			n.delivered = slices.DeleteFunc(n.delivered, func(existing string) bool { return existing == notice.id })
			n.mu.Unlock()
			continue
		}
		n.mu.Lock()
		if slices.Contains(n.delivered, notice.id) {
			n.shown[native] = notice.id
		} else {
			// Removed while the notice was being shown.
			removed = append(removed, native)
		}
		n.mu.Unlock()
	}
	for _, native := range removed {
		n.backend.close(native)
	}
}

func (n *Notifications) watchEvents() {
	events := n.backend.events()
	for {
		select {
		case <-n.done:
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			n.mu.Lock()
			id, known := n.shown[event.id]
			if !event.activated {
				delete(n.shown, event.id)
			}
			n.mu.Unlock()
			if !known || !event.activated {
				continue
			}
			if taskID, _, ok := reminders.NotificationTaskID(id); ok {
				select {
				case n.opens <- taskID:
				default:
				}
			}
		}
	}
}

const (
	notifyBusName = "org.freedesktop.Notifications"
	notifyPath    = dbus.ObjectPath("/org/freedesktop/Notifications")
	notifyIface   = "org.freedesktop.Notifications"
)

// dbusNotifications talks to org.freedesktop.Notifications on the session bus.
// It sends only the fixed summary and the task title the person wrote.
type dbusNotifications struct {
	mu   sync.Mutex
	conn *dbus.Conn
	out  chan notificationEvent
}

func newDBusNotifications() *dbusNotifications {
	return &dbusNotifications{out: make(chan notificationEvent, 16)}
}

func (d *dbusNotifications) connection(ctx context.Context) (*dbus.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn != nil && d.conn.Connected() {
		return d.conn, nil
	}
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	for _, member := range []string{"ActionInvoked", "NotificationClosed"} {
		if err := conn.AddMatchSignalContext(ctx, dbus.WithMatchObjectPath(notifyPath), dbus.WithMatchInterface(notifyIface), dbus.WithMatchMember(member)); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	go d.forward(signals)
	d.conn = conn
	return conn, nil
}

func (d *dbusNotifications) forward(signals <-chan *dbus.Signal) {
	for signal := range signals {
		if signal == nil || len(signal.Body) == 0 {
			continue
		}
		id, ok := signal.Body[0].(uint32)
		if !ok {
			continue
		}
		event := notificationEvent{id: id}
		switch signal.Name {
		case notifyIface + ".ActionInvoked":
			event.activated = true
		case notifyIface + ".NotificationClosed":
		default:
			continue
		}
		select {
		case d.out <- event:
		default:
		}
	}
}

func (d *dbusNotifications) available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := d.connection(ctx)
	if err != nil {
		return false
	}
	var name, vendor, version, spec string
	return conn.Object(notifyBusName, notifyPath).CallWithContext(ctx, notifyIface+".GetServerInformation", 0).Store(&name, &vendor, &version, &spec) == nil
}

// notificationMarkup escapes text for servers that render body markup.
var notificationMarkup = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func (d *dbusNotifications) show(ctx context.Context, summary, body string) (uint32, error) {
	conn, err := d.connection(ctx)
	if err != nil {
		return 0, reminders.ErrUnavailable
	}
	hints := map[string]dbus.Variant{
		"desktop-entry": dbus.MakeVariant("blur"),
		"urgency":       dbus.MakeVariant(byte(1)),
	}
	var id uint32
	err = conn.Object(notifyBusName, notifyPath).CallWithContext(ctx, notifyIface+".Notify", 0,
		"Blur", uint32(0), "", summary, notificationMarkup.Replace(body), []string{"default", "Open"}, hints, int32(-1)).Store(&id)
	if err != nil {
		return 0, reminders.ErrUnavailable
	}
	return id, nil
}

func (d *dbusNotifications) close(id uint32) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := d.connection(ctx)
	if err != nil {
		return
	}
	_ = conn.Object(notifyBusName, notifyPath).CallWithContext(ctx, notifyIface+".CloseNotification", 0, id).Err
}

func (d *dbusNotifications) events() <-chan notificationEvent { return d.out }
