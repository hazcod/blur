//go:build linux

package linux

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

const syntheticPayload = "fixture-only-sensitive-payload"

type syntheticA11yBus struct{ address string }

func (b syntheticA11yBus) GetAddress() (string, *dbus.Error) { return b.address, nil }

type syntheticRegistry struct {
	mu              sync.Mutex
	registered      map[string]bool
	deregistered    map[string]bool
	registerCount   map[string]int
	deregisterCount map[string]int
	changed         chan struct{}
}

func newSyntheticRegistry() *syntheticRegistry {
	return &syntheticRegistry{
		registered: make(map[string]bool), deregistered: make(map[string]bool),
		registerCount: make(map[string]int), deregisterCount: make(map[string]int), changed: make(chan struct{}, 32),
	}
}

func (r *syntheticRegistry) RegisterEvent(event string, _ []string, _ string) *dbus.Error {
	r.mu.Lock()
	r.registered[event] = true
	r.registerCount[event]++
	r.mu.Unlock()
	select {
	case r.changed <- struct{}{}:
	default:
	}
	return nil
}

func (r *syntheticRegistry) DeregisterEvent(event string, _ string) *dbus.Error {
	r.mu.Lock()
	r.deregistered[event] = true
	r.deregisterCount[event]++
	r.mu.Unlock()
	select {
	case r.changed <- struct{}{}:
	default:
	}
	return nil
}

func (r *syntheticRegistry) hasAllEvents(events []atspiSubscription, registered bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	set := r.deregistered
	if registered {
		set = r.registered
	}
	for _, event := range events {
		if !set[event.eventType] {
			return false
		}
	}
	return true
}

func (r *syntheticRegistry) countsAtLeast(events []atspiSubscription, count int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, event := range events {
		if r.registerCount[event.eventType] < count || r.deregisterCount[event.eventType] < count-1 {
			return false
		}
	}
	return true
}

type syntheticAccessible struct {
	bus  string
	path dbus.ObjectPath
	role uint32
	name string
}

func (a syntheticAccessible) GetRole() uint32 { return a.role }

func (a syntheticAccessible) GetName() string { return a.name }

func (a syntheticAccessible) GetParent() (string, dbus.ObjectPath) { return a.bus, a.path }

func startPrivateTestBus(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("dbus-daemon")
	if err != nil {
		if os.Getenv("BLUR_REQUIRE_DBUS_FIXTURE") == "1" {
			t.Fatal("dbus-daemon is required for the Linux AT-SPI integration fixture")
		}
		t.Skip("dbus-daemon is unavailable; Linux AT-SPI integration fixture skipped")
	}
	cmd := exec.Command(path, "--session", "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal("cannot create private D-Bus fixture")
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal("cannot start private D-Bus fixture")
	}
	finished := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			finished <- scanner.Text()
			return
		}
		finished <- ""
	}()
	var address string
	select {
	case address = <-finished:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("private D-Bus fixture did not publish an address")
	}
	if address == "" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("private D-Bus fixture returned no address")
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return address
}

func connectSyntheticBus(t *testing.T, address string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal("cannot connect private D-Bus fixture")
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func requestSyntheticName(t *testing.T, conn *dbus.Conn, name string) {
	t.Helper()
	if _, err := conn.RequestName(name, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal("cannot own synthetic D-Bus fixture name")
	}
}

func TestAdapterPrivateDBusFocusAndSuspendLifecycle(t *testing.T) {
	sessionAddress := startPrivateTestBus(t)
	atspiAddress := startPrivateTestBus(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", sessionAddress)

	session, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal("cannot connect synthetic session bus")
	}
	t.Cleanup(func() { _ = session.Close() })
	if err := session.Export(syntheticA11yBus{address: atspiAddress}, a11ySessionPath, a11ySessionIface); err != nil {
		t.Fatal("cannot export synthetic AT-SPI bus locator")
	}
	requestSyntheticName(t, session, a11ySessionService)

	registryConn := connectSyntheticBus(t, atspiAddress)
	registry := newSyntheticRegistry()
	if err := registryConn.Export(registry, a11yRegistryPath, a11yRegistryIface); err != nil {
		t.Fatal("cannot export synthetic AT-SPI registry")
	}
	requestSyntheticName(t, registryConn, a11yRegistry)

	appConn := connectSyntheticBus(t, atspiAddress)
	appName := appConn.Names()[0]
	windowPath := dbus.ObjectPath("/fixture/window")
	focusPath := dbus.ObjectPath("/fixture/window/focus")
	if err := appConn.Export(syntheticAccessible{bus: appName, path: "/org/a11y/atspi/null", role: 69}, windowPath, a11yAccessible); err != nil {
		t.Fatal("cannot export synthetic window")
	}
	if err := appConn.Export(syntheticAccessible{bus: appName, path: windowPath, role: 29}, focusPath, a11yAccessible); err != nil {
		t.Fatal("cannot export synthetic focused object")
	}

	adapter := New()
	t.Cleanup(func() { _ = adapter.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if report, err := adapter.Capability(ctx); err != nil || report.State != "available" {
		t.Fatalf("private fixture capability = %#v, err=%v", report, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !registry.hasAllEvents(atspiSubscriptions(), true) && time.Now().Before(deadline) {
		select {
		case <-registry.changed:
		case <-ctx.Done():
			t.Fatal("AT-SPI event registrations did not arrive")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !registry.hasAllEvents(atspiSubscriptions(), true) {
		t.Fatal("adapter did not register the expected passive AT-SPI events")
	}
	if err := appConn.Emit(focusPath, atspiFocusSignal, syntheticPayload); err != nil {
		t.Fatal("cannot emit synthetic focus event")
	}
	event, err := adapter.Next(ctx)
	if err != nil || event.Kind != "focus" || event.TargetID == "" || strings.Contains(fmt.Sprint(event), syntheticPayload) {
		t.Fatalf("synthetic focus event leaked payload or failed: event=%#v err=%v", event, err)
	}
	probe, err := adapter.Probe(ctx)
	if err != nil || probe.TargetID != event.TargetID || probe.Private || probe.Elevated || strings.Contains(fmt.Sprint(probe), syntheticPayload) {
		t.Fatalf("synthetic probe failed or leaked payload: probe=%#v err=%v", probe, err)
	}
	if err := adapter.Suspend(ctx); err != nil {
		t.Fatalf("Suspend(): %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for !registry.hasAllEvents(atspiSubscriptions(), false) && time.Now().Before(deadline) {
		select {
		case <-registry.changed:
		case <-ctx.Done():
			t.Fatal("AT-SPI deregistrations did not arrive")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !registry.hasAllEvents(atspiSubscriptions(), false) {
		t.Fatal("Suspend did not deregister all passive AT-SPI events")
	}
	if report, err := adapter.Capability(ctx); err != nil || report.State != "available" {
		t.Fatalf("capability after resume = %#v, err=%v", report, err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for !registry.countsAtLeast(atspiSubscriptions(), 2) && time.Now().Before(deadline) {
		select {
		case <-registry.changed:
		case <-ctx.Done():
			t.Fatal("AT-SPI events were not re-registered after resume")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !registry.countsAtLeast(atspiSubscriptions(), 2) {
		t.Fatal("resume did not re-register passive AT-SPI events")
	}
}

func TestPrivateBrowserWindowUsesOnlyAccessibleWindowName(t *testing.T) {
	address := startPrivateTestBus(t)
	conn := connectSyntheticBus(t, address)
	name := conn.Names()[0]
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tests := []struct {
		name    string
		appID   string
		window  string
		export  bool
		private bool
	}{
		{name: "regular page", appID: "google-chrome", window: "Roadmap — Google Chrome", export: true},
		{name: "incognito", appID: "google-chrome", window: "Roadmap — Google Chrome (Incognito)", export: true, private: true},
		{name: "private Firefox", appID: "firefox", window: "Report — Private Browsing", export: true, private: true},
		{name: "empty window name", appID: "chromium", export: true, private: true},
		{name: "unavailable window name", appID: "chromium", window: "Unused", private: true},
		{name: "ordinary application", appID: "org.gnome.TextEditor", window: "Private Browsing", private: false},
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := dbus.ObjectPath(fmt.Sprintf("/fixture/window%d", i))
			if test.export {
				if err := conn.Export(syntheticAccessible{name: test.window}, path, a11yAccessible); err != nil {
					t.Fatal("cannot export synthetic accessible window")
				}
			}
			got := IsPrivateBrowserWindow(ctx, conn, name, path, test.appID)
			if got != test.private {
				t.Fatalf("IsPrivateBrowserWindow() = %t, want %t", got, test.private)
			}
		})
	}
}
