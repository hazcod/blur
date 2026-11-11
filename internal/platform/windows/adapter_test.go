package windows

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/platform"
)

func TestWindowsBrowserTitlePolicyFailsClosedOnlyForKnownBrowsers(t *testing.T) {
	tests := []struct {
		name     string
		appID    string
		title    string
		titleErr error
		private  bool
	}{
		{name: "regular Chrome window", appID: "chrome.exe", title: "Roadmap - Google Chrome"},
		{name: "Incognito Chrome window", appID: "chrome.exe", title: "Roadmap - Google Chrome (Incognito)", private: true},
		{name: "InPrivate Edge window", appID: "msedge.exe", title: "Roadmap - InPrivate - Microsoft Edge", private: true},
		{name: "unreadable browser title", appID: "firefox.exe", titleErr: errors.New("fixture"), private: true},
		{name: "empty browser title", appID: "brave.exe", private: true},
		{name: "oversized browser title", appID: "chrome.exe", title: strings.Repeat("x", 513), private: true},
		{name: "invalid UTF-8 browser title", appID: "chrome.exe", title: string([]byte{0xff}), private: true},
		{name: "ordinary app title is irrelevant", appID: "notepad.exe", title: "Private Browsing", private: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isPrivateBrowserWindow(test.appID, test.title, test.titleErr); got != test.private {
				t.Fatalf("isPrivateBrowserWindow() = %t, want %t", got, test.private)
			}
		})
	}
}

func TestDecodeForegroundWindowTitleBoundsAndValidatesUTF16(t *testing.T) {
	tests := []struct {
		name   string
		buffer []uint16
		read   int
		want   string
		bad    bool
	}{
		{name: "unicode title", buffer: []uint16{'P', 0xD83D, 0xDE00}, read: 3, want: "P😀"},
		{name: "empty title", buffer: []uint16{0}, read: 0, bad: true},
		{name: "truncated title", buffer: []uint16{'x'}, read: 512, bad: true},
		{name: "length exceeds buffer", buffer: []uint16{'x'}, read: 2, bad: true},
		{name: "unpaired high surrogate", buffer: []uint16{0xD800}, read: 1, bad: true},
		{name: "unpaired low surrogate", buffer: []uint16{0xDC00}, read: 1, bad: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeForegroundWindowTitle(test.buffer, test.read)
			if (err != nil) != test.bad || got != test.want {
				t.Fatalf("decodeForegroundWindowTitle() = (%q, %v), want (%q, bad=%t)", got, err, test.want, test.bad)
			}
		})
	}
}

type fakeCaptureBackend struct {
	target        FocusedTarget
	events        chan platform.CaptureEvent
	subscribed    atomic.Int32
	stopped       atomic.Int32
	capability    platform.CapabilityReport
	foregroundErr error
}

func (b *fakeCaptureBackend) Capability(context.Context) (platform.CapabilityReport, error) {
	return b.capability, nil
}

func (b *fakeCaptureBackend) Foreground(context.Context) (FocusedTarget, error) {
	if b.foregroundErr != nil {
		return FocusedTarget{}, b.foregroundErr
	}
	return b.target, nil
}

func (b *fakeCaptureBackend) Subscribe(context.Context) (eventSubscription, error) {
	b.subscribed.Add(1)
	done := make(chan struct{})
	var once sync.Once
	return eventSubscription{events: b.events, stop: func() {
		b.stopped.Add(1)
		once.Do(func() { close(done) })
	}, done: done}, nil
}

func readyBackend() *fakeCaptureBackend {
	return &fakeCaptureBackend{
		target:     FocusedTarget{Window: 42, ProcessID: 7, AppID: "notepad", CreationTime: 1234},
		events:     make(chan platform.CaptureEvent, 2),
		capability: platform.CapabilityReport{ID: "capture", State: platform.Available, Reason: "available", Evidence: "ui_automation"},
	}
}

func TestProbeUsesOpaqueTargetAndStableExecutableIdentity(t *testing.T) {
	backend := readyBackend()
	adapter := newAdapter(backend)
	probe, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if probe.AppID != "notepad" || probe.TargetID == "" || strings.Contains(probe.TargetID, probe.AppID) {
		t.Fatalf("probe leaks identity or misses executable name: %#v", probe)
	}
	if !validTargetID(probe.TargetID) {
		t.Fatalf("adapter emitted invalid opaque target id %q", probe.TargetID)
	}
	called := false
	if err := adapter.WithVerifiedTarget(context.Background(), probe.TargetID, func(_ context.Context, target FocusedTarget) error {
		called = true
		if target != backend.target {
			t.Fatalf("borrowed target = %#v, want %#v", target, backend.target)
		}
		return nil
	}); err != nil || !called {
		t.Fatalf("WithVerifiedTarget() error=%v, callback called=%t", err, called)
	}
}

func TestProbeBlocksPrivateBrowserWindowFromVerifiedReads(t *testing.T) {
	backend := readyBackend()
	backend.target = FocusedTarget{Window: 42, ProcessID: 7, AppID: "chrome.exe", CreationTime: 1234, Private: true}
	adapter := newAdapter(backend)
	probe, err := adapter.Probe(context.Background())
	if err != nil || !probe.Private {
		t.Fatalf("private browser probe = %#v, %v; want Private=true", probe, err)
	}
	called := false
	err = adapter.WithVerifiedTarget(context.Background(), probe.TargetID, func(context.Context, FocusedTarget) error {
		called = true
		return nil
	})
	if !errors.Is(err, platform.ErrInvalidLease) || called {
		t.Fatalf("private browser target read: err=%v callback=%t", err, called)
	}
}

func TestVerifiedLeaseRejectsWindowThatBecamePrivate(t *testing.T) {
	backend := readyBackend()
	backend.target.AppID = "chrome.exe"
	adapter := newAdapter(backend)
	probe, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	backend.target.Private = true
	called := false
	err = adapter.WithVerifiedTarget(context.Background(), probe.TargetID, func(context.Context, FocusedTarget) error {
		called = true
		return nil
	})
	if !errors.Is(err, platform.ErrInvalidLease) || called {
		t.Fatalf("window that became private was read: err=%v callback=%t", err, called)
	}
}

func TestWindowsCaptureCapabilityReportsImplementedUIAutomationBoundary(t *testing.T) {
	report, err := windowsCaptureCapability(context.Background())
	if err != nil || report.ID != "capture" || report.State != platform.Available || report.Reason != "available" || report.Evidence != "ui_automation" {
		t.Fatalf("Windows capture capability = %#v, %v", report, err)
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("Windows capture capability is invalid: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := windowsCaptureCapability(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled capability context error = %v, want canceled", err)
	}
}

func TestVerifyTargetRejectsForegroundSwitch(t *testing.T) {
	backend := readyBackend()
	adapter := newAdapter(backend)
	probe, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.WithVerifiedTarget(context.Background(), probe.TargetID, func(context.Context, FocusedTarget) error {
		backend.target.Window++
		return nil
	})
	if !errors.Is(err, platform.ErrInvalidLease) {
		t.Fatalf("WithVerifiedTarget() error=%v, want stale-target rejection", err)
	}
}

func TestElevatedTargetIsNeverRead(t *testing.T) {
	backend := readyBackend()
	backend.target.Elevated = true
	adapter := newAdapter(backend)
	probe, err := adapter.Probe(context.Background())
	if err != nil || !probe.Elevated {
		t.Fatalf("elevated probe = %#v, %v", probe, err)
	}
	called := false
	err = adapter.WithVerifiedTarget(context.Background(), probe.TargetID, func(context.Context, FocusedTarget) error { called = true; return nil })
	if !errors.Is(err, platform.ErrInvalidLease) || called {
		t.Fatalf("elevated read err=%v callback=%t; want refusal before read", err, called)
	}
}

func TestNextCoalescesForegroundAndFocusEvents(t *testing.T) {
	backend := readyBackend()
	adapter := newAdapter(backend)
	defer adapter.Suspend(context.Background())
	backend.events <- platform.CaptureEvent{Kind: "frontmost_app_changed"}
	backend.events <- platform.CaptureEvent{Kind: "focused_context_changed"}
	event, err := adapter.Next(context.Background())
	if err != nil || event.Kind != "focused_context_changed" {
		t.Fatalf("Next() = %#v, %v", event, err)
	}
	if backend.subscribed.Load() != 1 {
		t.Fatalf("event subscriptions=%d, want one", backend.subscribed.Load())
	}
}

func TestSuspendReleasesWinEventHooks(t *testing.T) {
	backend := readyBackend()
	adapter := newAdapter(backend)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := adapter.Next(ctx); err == nil {
		t.Fatal("Next unexpectedly returned without an event or cancellation")
	}
	if err := adapter.Suspend(context.Background()); err != nil {
		t.Fatal(err)
	}
	if backend.stopped.Load() != 1 {
		t.Fatalf("stopped subscriptions=%d, want one", backend.stopped.Load())
	}
}

func TestWinEventMappingDropsEmptyAndUnknownEvents(t *testing.T) {
	for _, test := range []struct {
		name   string
		event  uint32
		window uintptr
		want   string
	}{
		{"foreground", 0x0003, 42, "frontmost_app_changed"},
		{"focus", 0x8005, 42, "focused_context_changed"},
		{"zero hwnd", 0x0003, 0, ""},
		{"unknown", 0x8007, 42, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := winEventKind(test.event, test.window); got != test.want {
				t.Fatalf("winEventKind(%#x, %d) = %q, want %q", test.event, test.window, got, test.want)
			}
		})
	}
}

func TestNextReturnsStructuredErrorWhenCapabilityIsUnavailable(t *testing.T) {
	backend := readyBackend()
	backend.capability.State = platform.UserDisabled
	backend.capability.Reason = "adapter_unimplemented"
	adapter := newAdapter(backend)

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err := adapter.Next(ctx)
	var unavailable *platform.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Next() error = %v, want structured unavailable result", err)
	}
	if unavailable.Report != backend.capability || backend.subscribed.Load() != 0 {
		t.Fatalf("Next() report=%#v subscriptions=%d; want capability and no native hooks", unavailable.Report, backend.subscribed.Load())
	}
}

func TestExecutableNameIsStableAndPathFree(t *testing.T) {
	for _, test := range []struct{ path, want string }{
		{`C:\Program Files\Editor\Editor.EXE`, "editor.exe"},
		{`/usr/local/bin/NOTEPAD.exe`, "notepad.exe"},
		{`Terminal.EXE`, "terminal.exe"},
	} {
		if got := executableAppID(test.path); got != test.want {
			t.Errorf("executableAppID(%q) = %q, want %q", test.path, got, test.want)
		}
	}
	if got := executableAppID(`C:\Users\Alice\`); got != "" {
		t.Fatalf("directory-only executable identity = %q, want empty", got)
	}
}
