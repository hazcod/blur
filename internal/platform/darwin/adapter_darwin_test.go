package darwin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ebitengine/purego/objc"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/platform"
)

func TestCapabilityReportsAccessibilityPermissionWithoutPrompting(t *testing.T) {
	tests := []struct {
		name    string
		trusted bool
		want    platform.CapabilityState
		why     string
	}{
		{"trusted capture available", true, platform.Available, "available"},
		{"permission not granted", false, platform.PermissionRequired, "permission_required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Adapter{trusted: func() (bool, error) { return tt.trusted, nil }}
			report, err := a.Capability(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if report.State != tt.want || report.Reason != tt.why || report.Evidence != "axui_element" {
				t.Fatalf("unexpected capability report: %+v", report)
			}
		})
	}
}

func TestCapabilityFailsClosedWithoutPermissionProbe(t *testing.T) {
	for name, adapter := range map[string]*Adapter{"nil adapter": nil, "missing probe": {}} {
		t.Run(name, func(t *testing.T) {
			report, err := adapter.Capability(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if report.State != platform.TemporarilyUnresponsive || report.Reason != "temporarily_unresponsive" {
				t.Fatalf("missing trust probe claimed capture capability: %+v", report)
			}
		})
	}
}

func TestPermissionGuidanceIsExplicitAndContextBound(t *testing.T) {
	called := 0
	a := &Adapter{requestPermission: func() error { called++; return nil }}
	if err := a.RequestPermission(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("native guidance calls = %d, want 1", called)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.RequestPermission(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request error = %v, want context.Canceled", err)
	}
	if called != 1 {
		t.Fatalf("canceled request reached native guidance, calls = %d", called)
	}
}

func TestAccessibilityPermissionUsesDarwinBooleanValue(t *testing.T) {
	if accessibilityTrustedValue(0) {
		t.Fatal("zero AX Boolean must mean permission denied")
	}
	if !accessibilityTrustedValue(1) {
		t.Fatal("nonzero AX Boolean must mean permission granted")
	}
}

func TestFrontmostTargetQueryRunsThroughMainThreadDispatcher(t *testing.T) {
	dispatched := false
	read := false
	appID, pid, err := queryFrontmostTargetOnMainThread(func(run func()) error {
		dispatched = true
		run()
		return nil
	}, func() (string, int32, error) {
		read = true
		return "com.example.editor", 42, nil
	})
	if err != nil || !dispatched || !read || appID != "com.example.editor" || pid != 42 {
		t.Fatalf("main-thread target query = (%q, %d, %v), dispatched=%t read=%t", appID, pid, err, dispatched, read)
	}
}

func TestFrontmostTargetQueryPropagatesDispatcherAndReadErrors(t *testing.T) {
	want := errors.New("main thread unavailable")
	if _, _, err := queryFrontmostTargetOnMainThread(func(func()) error { return want }, func() (string, int32, error) {
		return "", 0, nil
	}); !errors.Is(err, want) {
		t.Fatalf("dispatcher error = %v, want %v", err, want)
	}
	if _, _, err := queryFrontmostTargetOnMainThread(func(run func()) error { run(); return nil }, func() (string, int32, error) {
		return "", 0, want
	}); !errors.Is(err, want) {
		t.Fatalf("frontmost read error = %v, want %v", err, want)
	}
}

func TestFrontmostNativeCallStaysOnPinnedOSThreadUntilCleanup(t *testing.T) {
	var events []string
	want := errors.New("native dispatch failed")
	err := withFrontmostOSThread(func() { events = append(events, "lock") }, func() { events = append(events, "unlock") }, func() error {
		events = append(events, "native")
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("pinned native call error = %v, want %v", err, want)
	}
	if got, expected := strings.Join(events, ","), "lock,native,unlock"; got != expected {
		t.Fatalf("pinned native call order = %q, want %q", got, expected)
	}
}

func TestActivationObserverNativeCallsStayOnPinnedOSThread(t *testing.T) {
	var events []string
	err := withPinnedOSThread(
		func() { events = append(events, "lock") },
		func() { events = append(events, "unlock") },
		func() error {
			events = append(events, "autorelease-pool")
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(events, ","), "lock,autorelease-pool,unlock"; got != want {
		t.Fatalf("activation native call order = %q, want %q", got, want)
	}
}

func TestNativeAccessibilityPermissionAPIsResolveWithoutPrompting(t *testing.T) {
	if _, err := nativeAccessibilityTrusted(); err != nil {
		t.Fatalf("non-interactive Accessibility trust API: %v", err)
	}
	accessibilityPrompt.once.Do(loadAccessibilityPrompt)
	if accessibilityPrompt.err != nil || accessibilityPrompt.call == nil {
		t.Fatalf("native Accessibility prompt API did not resolve: %v", accessibilityPrompt.err)
	}
}

func TestNativeFrontmostProbeABISmoke(t *testing.T) {
	if os.Getenv("BLUR_TEST_DARWIN_FRONTMOST") != "1" {
		t.Skip("set BLUR_TEST_DARWIN_FRONTMOST=1 for a read-only AppKit ABI smoke test")
	}
	appID, pid, err := nativeFrontmostTarget()
	if err != nil {
		t.Fatal(err)
	}
	if !validBundleID(appID) || pid <= 0 {
		t.Fatalf("invalid frontmost metadata: appID=%q pid=%d", appID, pid)
	}
}

func TestProbeReturnsOnlyNormalizedAppAndOpaqueTarget(t *testing.T) {
	a := &Adapter{
		trusted:       func() (bool, error) { return true, nil },
		currentTarget: func() (string, int32, error) { return "Com.Example.Editor", 8123, nil },
	}
	probe, err := a.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if probe.AppID != "com.example.editor" || probe.Domain != "" || probe.Private || probe.Secure || probe.Elevated {
		t.Fatalf("unexpected app-only probe: %+v", probe)
	}
	if !regexp.MustCompile(`^darwin:[0-9a-f]{32}$`).MatchString(probe.TargetID) || probe.TargetID == "darwin:8123" {
		t.Fatalf("target ID is not opaque: %q", probe.TargetID)
	}
}

func TestProbeMarksBlurPrivateButAllowsBrowserForWindowPrivacyGate(t *testing.T) {
	for _, test := range []struct {
		appID       string
		wantPrivate bool
	}{{"com.google.chrome", false}, {"com.apple.safari", false}, {appidentity.BundleID, true}} {
		t.Run(test.appID, func(t *testing.T) {
			a := &Adapter{trusted: func() (bool, error) { return true, nil }, currentTarget: func() (string, int32, error) { return test.appID, 10, nil }}
			probe, err := a.Probe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if probe.Private != test.wantPrivate {
				t.Fatalf("probe.Private = %t, want %t: %+v", probe.Private, test.wantPrivate, probe)
			}
		})
	}
}

func TestProbeDoesNotResolveTargetWithoutPermission(t *testing.T) {
	called := false
	a := &Adapter{
		trusted:       func() (bool, error) { return false, nil },
		currentTarget: func() (string, int32, error) { called = true; return "org.example.app", 10, nil },
	}
	_, err := a.Probe(context.Background())
	var unavailable *platform.UnavailableError
	if !errors.As(err, &unavailable) || unavailable.Report.State != platform.PermissionRequired || called {
		t.Fatalf("permission denial did not fail before probe: err=%v called=%t", err, called)
	}
}

func TestProbeRejectsMalformedMetadataWithoutReturningIt(t *testing.T) {
	for _, target := range []struct {
		app string
		pid int32
	}{{"", 1}, {"org.example/path", 1}, {"org.example.app", 0}, {"org.example.app", -1}} {
		a := &Adapter{trusted: func() (bool, error) { return true, nil }, currentTarget: func() (string, int32, error) { return target.app, target.pid, nil }}
		probe, err := a.Probe(context.Background())
		if err == nil || probe.TargetID != "" || probe.AppID != "" {
			t.Fatalf("malformed target escaped: target=%+v err=%v", probe, err)
		}
	}
}

func TestProbeReturnsCancellationAfterNativeMetadataCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &Adapter{
		trusted:       func() (bool, error) { return true, nil },
		currentTarget: func() (string, int32, error) { cancel(); return "org.example.app", 10, nil },
	}
	if _, err := a.Probe(ctx); err != context.Canceled {
		t.Fatalf("got %v, want cancellation", err)
	}
}

func TestProbeTargetVerificationRejectsStaleOrSwitchedFocus(t *testing.T) {
	appID, pid := "org.example.editor", int32(10)
	a := &Adapter{trusted: func() (bool, error) { return true, nil }, currentTarget: func() (string, int32, error) { return appID, pid, nil }}
	probe, err := a.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.VerifyTarget(context.Background(), probe.TargetID); err != nil {
		t.Fatalf("current target rejected: %v", err)
	}
	if err := a.VerifyTarget(context.Background(), "darwin:00000000000000000000000000000000"); err == nil {
		t.Fatal("stale target token was accepted")
	}
	appID, pid = "org.example.other", 11
	if err := a.VerifyTarget(context.Background(), probe.TargetID); err == nil {
		t.Fatal("changed frontmost process was accepted")
	}
}

func TestWithVerifiedTargetScopesPIDAndRejectsFocusChangeAfterRead(t *testing.T) {
	appID, pid := "org.example.editor", int32(40)
	a := &Adapter{trusted: func() (bool, error) { return true, nil }, currentTarget: func() (string, int32, error) { return appID, pid, nil }}
	probe, err := a.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = a.WithVerifiedTarget(context.Background(), probe.TargetID, func(_ context.Context, targetPID int32) error {
		called = true
		if targetPID != 40 {
			t.Fatalf("scoped PID = %d, want 40", targetPID)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("verified read failed: called=%t err=%v", called, err)
	}
	err = a.WithVerifiedTarget(context.Background(), probe.TargetID, func(context.Context, int32) error {
		pid = 41
		return nil
	})
	if !errors.Is(err, platform.ErrInvalidLease) {
		t.Fatalf("focus change after read accepted: %v", err)
	}
	pid = 40
	err = a.WithVerifiedTarget(context.Background(), probe.TargetID, func(context.Context, int32) error {
		pid = 42
		return fmt.Errorf("read failed")
	})
	if !errors.Is(err, platform.ErrInvalidLease) {
		t.Fatalf("focus change was hidden by read error: %v", err)
	}
}

func TestNextBlocksForActivationAndReturnsContentFreeEvent(t *testing.T) {
	registered := make(chan chan<- struct{}, 1)
	a := &Adapter{subscribeActivation: func(events chan<- struct{}) (func(), error) {
		registered <- events
		return func() {}, nil
	}}
	result := make(chan struct {
		event platform.CaptureEvent
		err   error
	}, 1)
	go func() {
		event, err := a.Next(context.Background())
		result <- struct {
			event platform.CaptureEvent
			err   error
		}{event, err}
	}()
	events := <-registered
	select {
	case got := <-result:
		t.Fatalf("Next returned without an activation: %+v", got)
	case <-time.After(15 * time.Millisecond):
	}
	events <- struct{}{}
	got := <-result
	if got.err != nil || got.event.Kind != "frontmost_app_changed" || got.event.TargetID != "" {
		t.Fatalf("unexpected activation event: %+v err=%v", got.event, got.err)
	}
}

func TestNextHonorsCancellationWithoutPolling(t *testing.T) {
	registered := make(chan struct{}, 1)
	a := &Adapter{subscribeActivation: func(chan<- struct{}) (func(), error) {
		registered <- struct{}{}
		return func() {}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := a.Next(ctx); result <- err }()
	<-registered
	cancel()
	if err := <-result; err != context.Canceled {
		t.Fatalf("got %v, want context cancellation", err)
	}
}

func TestCloseUnblocksNextAndUnregistersListenerOnce(t *testing.T) {
	registered := make(chan struct{}, 1)
	stopped := 0
	a := &Adapter{subscribeActivation: func(chan<- struct{}) (func(), error) {
		registered <- struct{}{}
		return func() { stopped++ }, nil
	}}
	result := make(chan error, 1)
	go func() { _, err := a.Next(context.Background()); result <- err }()
	<-registered
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("Next remained blocked after close")
	}
	_ = a.Close()
	if stopped != 1 {
		t.Fatalf("listener stop count = %d, want 1", stopped)
	}
}

func TestSuspendUnregistersAndAllowsLaterActivationSubscription(t *testing.T) {
	registered := 0
	stopped := 0
	a := &Adapter{subscribeActivation: func(chan<- struct{}) (func(), error) {
		registered++
		return func() { stopped++ }, nil
	}}
	if err := a.ensureActivationEvents(); err != nil {
		t.Fatal(err)
	}
	if err := a.Suspend(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.ensureActivationEvents(); err != nil {
		t.Fatal(err)
	}
	if registered != 2 || stopped != 1 {
		t.Fatalf("after resume: registered=%d stopped=%d", registered, stopped)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if stopped != 2 {
		t.Fatalf("Close did not unregister resumed listener: stopped=%d", stopped)
	}
}

func TestActivationSubscriptionFailureDoesNotLeakNativeError(t *testing.T) {
	a := &Adapter{subscribeActivation: func(chan<- struct{}) (func(), error) {
		return nil, errors.New("private native detail")
	}}
	_, err := a.Next(context.Background())
	if err == nil || err.Error() != "darwin: activation observer unavailable" || strings.Contains(err.Error(), "private") {
		t.Fatalf("subscription error was not sanitized: %v", err)
	}
}

func TestNextSubscribesToCurrentAppAccessibilityFocusChanges(t *testing.T) {
	type registration struct {
		pid    int32
		signal chan<- struct{}
	}
	registered := make(chan registration, 1)
	stopped := make(chan struct{}, 1)
	a := &Adapter{
		trusted:             func() (bool, error) { return true, nil },
		currentTarget:       func() (string, int32, error) { return "org.example.editor", 321, nil },
		subscribeActivation: func(chan<- struct{}) (func(), error) { return func() {}, nil },
		watchFocus: func(pid int32, signal chan<- struct{}) (func(), error) {
			registered <- registration{pid: pid, signal: signal}
			return func() { stopped <- struct{}{} }, nil
		},
	}
	result := make(chan struct {
		event platform.CaptureEvent
		err   error
	}, 1)
	go func() {
		event, err := a.Next(context.Background())
		result <- struct {
			event platform.CaptureEvent
			err   error
		}{event, err}
	}()
	watch := <-registered
	if watch.pid != 321 {
		t.Fatalf("observer PID = %d, want current target", watch.pid)
	}
	watch.signal <- struct{}{}
	got := <-result
	if got.err != nil || got.event.Kind != "focused_context_changed" || got.event.TargetID != "" {
		t.Fatalf("unexpected focus-change event: %+v err=%v", got.event, got.err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("focus observer was not stopped after event")
	}
}

func TestNativeActivationObserverRegistrationSmoke(t *testing.T) {
	if os.Getenv("BLUR_TEST_DARWIN_ACTIVATION") != "1" {
		t.Skip("set BLUR_TEST_DARWIN_ACTIVATION=1 for a read-only observer registration smoke test")
	}
	a := New()
	if err := a.ensureActivationEvents(); err != nil {
		t.Fatal(err)
	}
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(objc.RegisterName("alloc"))
	pool = pool.Send(objc.RegisterName("init"))
	if pool == 0 {
		t.Fatal("autorelease pool unavailable")
	}
	defer pool.Send(objc.RegisterName("drain"))
	workspace := objc.ID(objc.GetClass("NSWorkspace")).Send(objc.RegisterName("sharedWorkspace"))
	center := workspace.Send(objc.RegisterName("notificationCenter"))
	name := objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), "NSWorkspaceDidActivateApplicationNotification")
	center.Send(objc.RegisterName("postNotificationName:object:userInfo:"), name, objc.ID(0), objc.ID(0))
	event, err := a.Next(context.Background())
	if err != nil || event.Kind != "frontmost_app_changed" {
		t.Fatalf("notification callback failed: event=%+v err=%v", event, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNextSubscribesToBrowserFocusChanges(t *testing.T) {
	var focusCalls int
	a := &Adapter{
		trusted:             func() (bool, error) { return true, nil },
		currentTarget:       func() (string, int32, error) { return "com.apple.safari", 55, nil },
		subscribeActivation: func(events chan<- struct{}) (func(), error) { events <- struct{}{}; return func() {}, nil },
		watchFocus:          func(int32, chan<- struct{}) (func(), error) { focusCalls++; return func() {}, nil },
	}
	event, err := a.Next(context.Background())
	if err != nil || event.Kind != "frontmost_app_changed" || focusCalls != 1 {
		t.Fatalf("browser focus changes were not observed: event=%+v err=%v focusCalls=%d", event, err, focusCalls)
	}
}

func TestNativeAXFocusObserverRegistrationSmoke(t *testing.T) {
	if os.Getenv("BLUR_TEST_DARWIN_AX_FOCUS") != "1" {
		t.Skip("set BLUR_TEST_DARWIN_AX_FOCUS=1 for read-only AXObserver registration")
	}
	trusted, err := nativeAccessibilityTrusted()
	if err != nil {
		t.Fatal("Accessibility permission probe failed")
	}
	if !trusted {
		t.Skip("Accessibility permission not granted")
	}
	_, pid, err := nativeFrontmostTarget()
	if err != nil {
		t.Fatal("frontmost target probe failed")
	}
	stop, err := nativeFocusSubscription(pid, make(chan struct{}, 1))
	if err != nil {
		t.Fatal(err)
	}
	stop()
}

func TestCapabilityPropagatesCancellationBeforeNativeCall(t *testing.T) {
	called := false
	a := &Adapter{trusted: func() (bool, error) { called = true; return false, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Capability(ctx); err != context.Canceled {
		t.Fatalf("got %v, want cancellation", err)
	}
	if called {
		t.Fatal("native permission check ran after cancellation")
	}
}

func TestCapabilityFailsClosedWhenPermissionProbeFails(t *testing.T) {
	a := &Adapter{trusted: func() (bool, error) { return false, context.DeadlineExceeded }}
	report, err := a.Capability(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.State != platform.TemporarilyUnresponsive || report.Reason != "temporarily_unresponsive" {
		t.Fatalf("permission probe error was not reported safely: %+v", report)
	}
}

func TestNativeFocusSubscriptionStopsPromptlyWhenStoppedImmediately(t *testing.T) {
	if os.Getenv("BLUR_TEST_DARWIN_AX_FOCUS") != "1" {
		t.Skip("set BLUR_TEST_DARWIN_AX_FOCUS=1 (needs main-thread dispatch and Accessibility)")
	}
	_, pid, err := nativeFrontmostTarget()
	if err != nil {
		t.Skip("frontmost target probe unavailable")
	}
	for i := 0; i < 20; i++ {
		stop, err := nativeFocusSubscription(pid, make(chan struct{}, 1))
		if err != nil {
			t.Skipf("focus observer unavailable: %v", err)
		}
		finished := make(chan struct{})
		go func() { stop(); close(finished) }()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Fatalf("stop hung on iteration %d", i)
		}
	}
}
