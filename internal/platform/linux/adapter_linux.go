package linux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/hazcod/blur/internal/platform"
)

const (
	a11ySessionService = "org.a11y.Bus"
	a11ySessionPath    = dbus.ObjectPath("/org/a11y/bus")
	a11ySessionIface   = "org.a11y.Bus"
	a11yRegistry       = "org.a11y.atspi.Registry"
	a11yRegistryPath   = dbus.ObjectPath("/org/a11y/atspi/registry")
	a11yRegistryIface  = "org.a11y.atspi.Registry"
	a11yDBus           = "org.freedesktop.DBus"
	a11yDBusPath       = dbus.ObjectPath("/org/freedesktop/DBus")
	a11yEventTimeout   = 300 * time.Millisecond
	a11yStartTimeout   = 3 * time.Second
	a11yMaxParentDepth = 32
)

var _ platform.CaptureSource = (*Adapter)(nil)
var _ platform.CaptureSuspender = (*Adapter)(nil)

// Adapter passively subscribes to AT-SPI focus and structural/text-change
// events on the per-user accessibility bus. It never invokes accessibility
// actions or performs app navigation.
type Adapter struct {
	mu         sync.Mutex
	conn       *dbus.Conn
	connCancel context.CancelFunc
	signals    chan *dbus.Signal
	events     chan platform.CaptureEvent
	done       chan struct{}
	started    bool
	closed     bool
	tracker    *focusTracker
}

// FocusedContext is a short-lived borrowed view. Use only inside
// WithVerifiedTarget's callback; do not retain or serialize its D-Bus identity.
type FocusedContext struct {
	Conn        *dbus.Conn
	FocusedBus  string
	FocusedPath dbus.ObjectPath
	WindowBus   string
	WindowPath  dbus.ObjectPath
}

func New() *Adapter {
	return &Adapter{events: make(chan platform.CaptureEvent, 1), tracker: newFocusTracker(nil)}
}

func (a *Adapter) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	report := platform.CapabilityReport{ID: "capture", State: platform.TemporarilyUnresponsive, Reason: "service_unavailable", Evidence: "at_spi2"}
	if ctx == nil {
		return report, platform.ErrInvalidCapabilityReport
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if a == nil {
		return report, nil
	}
	if err := a.ensureStarted(ctx); err != nil {
		return report, nil
	}
	report.State, report.Reason = platform.Available, "available"
	return report, nil
}

func (a *Adapter) Probe(ctx context.Context) (platform.CaptureProbe, error) {
	if ctx == nil {
		return platform.CaptureProbe{}, platform.ErrInvalidLease
	}
	if err := ctx.Err(); err != nil {
		return platform.CaptureProbe{}, err
	}
	if a == nil || a.tracker == nil {
		return platform.CaptureProbe{}, platform.ErrInvalidLease
	}
	target, ok := a.tracker.current()
	if !ok {
		return platform.CaptureProbe{}, a.unavailable()
	}
	return platform.CaptureProbe{
		TargetID: target.token,
		AppID:    target.appID,
		Private:  target.private,
		Elevated: target.elevated,
	}, nil
}

func (a *Adapter) Next(ctx context.Context) (platform.CaptureEvent, error) {
	if ctx == nil {
		return platform.CaptureEvent{}, platform.ErrInvalidLease
	}
	if err := ctx.Err(); err != nil {
		return platform.CaptureEvent{}, err
	}
	if err := a.ensureStarted(ctx); err != nil {
		return platform.CaptureEvent{}, a.unavailable()
	}
	select {
	case <-ctx.Done():
		return platform.CaptureEvent{}, ctx.Err()
	case <-a.done:
		return platform.CaptureEvent{}, a.unavailable()
	case event := <-a.events:
		if event.TargetID == "" {
			return platform.CaptureEvent{}, a.unavailable()
		}
		return event, nil
	}
}

func (a *Adapter) WithVerifiedTarget(ctx context.Context, token string, read func(context.Context, FocusedContext) error) error {
	if a == nil || ctx == nil || read == nil || a.tracker == nil {
		return platform.ErrInvalidLease
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	target, ok := a.tracker.target(token)
	if !ok {
		return platform.ErrInvalidLease
	}
	a.mu.Lock()
	conn, running := a.conn, a.started && !a.closed
	a.mu.Unlock()
	if !running || conn == nil {
		return platform.ErrInvalidLease
	}
	context := FocusedContext{
		Conn: conn, FocusedBus: target.focused.busName, FocusedPath: target.focused.path,
		WindowBus: target.window.busName, WindowPath: target.window.path,
	}
	readErr := read(ctx, context)
	if err := ctx.Err(); err != nil {
		return err
	}
	current, ok := a.tracker.target(token)
	if !ok || current.focused != target.focused || current.window != target.window {
		return platform.ErrInvalidLease
	}
	return readErr
}

func (a *Adapter) Close() error {
	return a.stop(context.Background(), true)
}

// Suspend releases the AT-SPI registration and private bus connection while
// capture is paused. Capability or Next will reconnect when the user resumes.
func (a *Adapter) Suspend(ctx context.Context) error {
	return a.stop(ctx, false)
}

func (a *Adapter) stop(parent context.Context, permanent bool) error {
	if a == nil {
		return nil
	}
	if parent == nil {
		return platform.ErrInvalidLease
	}
	a.mu.Lock()
	if a.closed && permanent {
		a.mu.Unlock()
		return nil
	}
	if permanent {
		a.closed = true
	}
	conn, signals, done, connCancel := a.conn, a.signals, a.done, a.connCancel
	a.conn, a.connCancel, a.signals = nil, nil, nil
	a.started = false
	a.mu.Unlock()
	a.tracker.clear()
	if conn == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	for _, spec := range atspiSubscriptions() {
		_ = conn.Object(a11yRegistry, a11yRegistryPath).CallWithContext(ctx, a11yRegistryIface+".DeregisterEvent", 0, spec.eventType, "").Err
	}
	conn.RemoveSignal(signals)
	_ = conn.Close()
	if connCancel != nil {
		connCancel()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Adapter) ensureStarted(parent context.Context) error {
	if a == nil || parent == nil || parent.Err() != nil {
		return platform.ErrInvalidLease
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return platform.ErrInvalidLease
	}
	if a.started {
		if a.conn != nil && a.conn.Connected() {
			return nil
		}
		if a.connCancel != nil {
			a.connCancel()
		}
		a.started, a.conn, a.connCancel, a.signals = false, nil, nil, nil
	}
	if a.events == nil {
		a.events = make(chan platform.CaptureEvent, 1)
	}
	if a.tracker == nil {
		a.tracker = newFocusTracker(nil)
	}
	ctx, cancel := context.WithTimeout(parent, a11yStartTimeout)
	defer cancel()
	conn, connCancel, err := connectATSPIDBus(ctx)
	if err != nil {
		return platform.ErrInvalidLease
	}
	signals := make(chan *dbus.Signal, 32)
	for _, spec := range atspiSubscriptions() {
		if err := conn.AddMatchSignalContext(ctx, dbus.WithMatchInterface(spec.iface), dbus.WithMatchMember(spec.member)); err != nil {
			conn.Close()
			connCancel()
			return platform.ErrInvalidLease
		}
	}
	conn.Signal(signals)
	registered := make([]atspiSubscription, 0, len(atspiSubscriptions()))
	for _, spec := range atspiSubscriptions() {
		if err := conn.Object(a11yRegistry, a11yRegistryPath).CallWithContext(ctx, a11yRegistryIface+".RegisterEvent", 0, spec.eventType, []string{}, "").Err; err != nil {
			for _, prior := range registered {
				_ = conn.Object(a11yRegistry, a11yRegistryPath).CallWithContext(ctx, a11yRegistryIface+".DeregisterEvent", 0, prior.eventType, "").Err
			}
			conn.RemoveSignal(signals)
			conn.Close()
			connCancel()
			return platform.ErrInvalidLease
		}
		registered = append(registered, spec)
	}
	done := make(chan struct{})
	a.conn, a.connCancel, a.signals, a.done, a.started = conn, connCancel, signals, done, true
	go a.runEvents(conn, signals, done)
	return nil
}

func connectATSPIDBus(ctx context.Context) (*dbus.Conn, context.CancelFunc, error) {
	session, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, nil, err
	}
	var address string
	err = session.Object(a11ySessionService, a11ySessionPath).CallWithContext(ctx, a11ySessionIface+".GetAddress", 0).Store(&address)
	_ = session.Close()
	if err != nil || !validATSPIDBusAddress(address) {
		return nil, nil, errors.New("AT-SPI bus unavailable")
	}
	// The connection's lifetime must outlive the short-lived setup context.
	// It is explicitly canceled by Suspend/Close after event deregistration.
	connCtx, cancel := context.WithCancel(context.Background())
	stopOnSetupTimeout := context.AfterFunc(ctx, cancel)
	conn, err := dbus.Connect(address, dbus.WithContext(connCtx))
	setupWasCanceled := !stopOnSetupTimeout() || ctx.Err() != nil
	if err != nil {
		cancel()
		return nil, nil, errors.New("AT-SPI bus unavailable")
	}
	if setupWasCanceled {
		_ = conn.Close()
		cancel()
		return nil, nil, errors.New("AT-SPI bus unavailable")
	}
	return conn, cancel, nil
}

func (a *Adapter) runEvents(conn *dbus.Conn, signals <-chan *dbus.Signal, done chan<- struct{}) {
	defer close(done)
	defer func() {
		a.mu.Lock()
		current := a.conn == conn
		if current {
			a.started, a.conn, a.connCancel, a.signals = false, nil, nil, nil
		}
		a.mu.Unlock()
		if current {
			a.tracker.clear()
		}
	}()
	for {
		select {
		case signal, ok := <-signals:
			if !ok {
				return
			}
			a.handleEventSignal(conn, signal)
		case <-conn.Context().Done():
			return
		}
	}
}

func (a *Adapter) handleEventSignal(conn *dbus.Conn, signal *dbus.Signal) {
	defer discardATSPISignalBody(signal)
	if focused, ok := focusRefFromSignal(signal); ok {
		ctx, cancel := context.WithTimeout(context.Background(), a11yEventTimeout)
		defer cancel()
		window, err := findATSPIWindow(ctx, conn, focused)
		if err != nil {
			a.tracker.clear()
			return
		}
		appID, elevated, err := atspiProcessMetadata(ctx, conn, focused.busName)
		if err != nil {
			a.tracker.clear()
			return
		}
		private := isPrivateLinuxApp(appID) || IsPrivateBrowserWindow(ctx, conn, window.busName, window.path, appID)
		event, err := a.tracker.updateWithStatus(focused, window, appID, private, elevated)
		if err != nil {
			return
		}
		a.enqueue(event)
		return
	}
	if !atspiRefreshSignal(signal) {
		return
	}
	active, ok := a.tracker.current()
	if !ok || signal.Sender != active.window.busName || !validObjectPath(signal.Path) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), a11yEventTimeout)
	defer cancel()
	window, err := findATSPIWindow(ctx, conn, atspiObjectRef{busName: signal.Sender, path: signal.Path})
	if err != nil || window != active.window {
		return
	}
	a.enqueue(platform.CaptureEvent{Kind: "content", TargetID: active.token})
}

func (a *Adapter) enqueue(event platform.CaptureEvent) {
	select {
	case a.events <- event:
		return
	default:
	}
	select {
	case <-a.events:
	default:
	}
	select {
	case a.events <- event:
	default:
	}
}

func findATSPIWindow(ctx context.Context, conn *dbus.Conn, start atspiObjectRef) (atspiObjectRef, error) {
	current := start
	for depth := 0; depth < a11yMaxParentDepth; depth++ {
		if err := ctx.Err(); err != nil {
			return atspiObjectRef{}, err
		}
		var role uint32
		if err := conn.Object(current.busName, current.path).CallWithContext(ctx, a11yAccessible+".GetRole", 0).Store(&role); err != nil {
			return atspiObjectRef{}, errors.New("AT-SPI role unavailable")
		}
		if role == 23 || role == 69 {
			return current, nil
		}
		var busName string
		var path dbus.ObjectPath
		if err := conn.Object(current.busName, current.path).CallWithContext(ctx, a11yAccessible+".GetParent", 0).Store(&busName, &path); err != nil {
			return atspiObjectRef{}, errors.New("AT-SPI parent unavailable")
		}
		parent := atspiObjectRef{busName: busName, path: path}
		if path == "/org/a11y/atspi/null" || !validATSPISender(parent.busName) || !validObjectPath(parent.path) {
			return atspiObjectRef{}, errors.New("AT-SPI window unavailable")
		}
		current = parent
	}
	return atspiObjectRef{}, errors.New("AT-SPI ancestry limit exceeded")
}

func atspiProcessMetadata(ctx context.Context, conn *dbus.Conn, busName string) (string, bool, error) {
	if !validATSPISender(busName) {
		return "", false, platform.ErrInvalidLease
	}
	var pid uint32
	if err := conn.Object(a11yDBus, a11yDBusPath).CallWithContext(ctx, a11yDBus+".GetConnectionUnixProcessID", 0, busName).Store(&pid); err != nil || pid == 0 {
		return "", false, platform.ErrInvalidLease
	}
	var uid uint32
	if err := conn.Object(a11yDBus, a11yDBusPath).CallWithContext(ctx, a11yDBus+".GetConnectionUnixUser", 0, busName).Store(&uid); err != nil {
		return "", false, platform.ErrInvalidLease
	}
	exe, err := os.Readlink(filepath.Join("/proc", uint32String(pid), "exe"))
	if err != nil {
		return "", false, platform.ErrInvalidLease
	}
	appID := strings.ToLower(filepath.Base(strings.TrimSuffix(exe, " (deleted)")))
	if !validLinuxAppID(appID) {
		return "", false, platform.ErrInvalidLease
	}
	return appID, uint64(uid) != uint64(os.Getuid()), nil
}

func uint32String(value uint32) string {
	if value == 0 {
		return "0"
	}
	var digits [10]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}

func (a *Adapter) unavailable() error {
	return &platform.UnavailableError{Report: platform.CapabilityReport{
		ID: "capture", State: platform.TemporarilyUnresponsive,
		Reason: "service_unavailable", Evidence: "at_spi2",
	}}
}
