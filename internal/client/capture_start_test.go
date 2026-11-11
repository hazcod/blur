package client

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/ingest"
	"github.com/hazcod/blur/internal/platform"
)

func TestPermissionWatcherRechecksOnActivationAndFallbackTickUntilStopped(t *testing.T) {
	events := make(chan struct{}, 2)
	ticks := make(chan time.Time, 2)
	stop := make(chan struct{})
	done := make(chan struct{})
	rechecks := make(chan struct{}, 3)
	finished := make(chan struct{})

	go func() {
		defer close(finished)
		watchPermissionRechecks(events, ticks, stop, done, func() { rechecks <- struct{}{} })
	}()

	events <- struct{}{}
	select {
	case <-rechecks:
	case <-time.After(time.Second):
		t.Fatal("first app activation did not recheck permission")
	}
	events <- struct{}{}
	select {
	case <-rechecks:
	case <-time.After(time.Second):
		t.Fatal("watcher stopped before permission was granted")
	}
	ticks <- time.Now()
	select {
	case <-rechecks:
	case <-time.After(time.Second):
		t.Fatal("fallback timer did not recheck permission")
	}
	close(stop)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("permission watcher did not stop")
	}
}

func TestPermissionWatchKeepsSlowFallbackWhenActivationObserverIsAvailable(t *testing.T) {
	if got := permissionWatchInterval(true); got != 10*time.Second {
		t.Fatalf("observer-backed fallback interval = %s, want 10s", got)
	}
	if got := permissionWatchInterval(false); got != 2*time.Second {
		t.Fatalf("poll-only fallback interval = %s, want 2s", got)
	}
}

type startupCaptureSource struct {
	state    platform.CapabilityState
	sequence []platform.CapabilityState
	checks   atomic.Int32
	started  atomic.Int32
	prompts  atomic.Int32
}

func (s *startupCaptureSource) Capability(context.Context) (platform.CapabilityReport, error) {
	state := s.state
	if len(s.sequence) > 0 {
		index := int(s.checks.Add(1)) - 1
		if index >= len(s.sequence) {
			index = len(s.sequence) - 1
		}
		state = s.sequence[index]
	}
	return platform.CapabilityReport{ID: "capture", State: state, Reason: "startup_test", Evidence: "fixture"}, nil
}

func (*startupCaptureSource) Suspend(context.Context) error { return nil }

func (s *startupCaptureSource) RequestPermission(context.Context) error {
	s.prompts.Add(1)
	return nil
}

func TestStartupCaptureStartsByDefaultOnlyWhenCapabilityIsAvailable(t *testing.T) {
	source := &startupCaptureSource{state: platform.Available}
	controller := ingest.NewController(source, func(context.Context) (func(context.Context) error, error) {
		return func(ctx context.Context) error {
			source.started.Add(1)
			<-ctx.Done()
			return ctx.Err()
		}, nil
	})
	if _, err := startCaptureOnLaunch(context.Background(), controller, true); err != nil {
		t.Fatalf("default-on capture startup: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for source.started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if source.started.Load() != 1 {
		t.Fatal("capture worker did not start")
	}
	if err := controller.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStartupCaptureRespectsPauseAndDoesNotPromptForPermission(t *testing.T) {
	source := &startupCaptureSource{state: platform.PermissionRequired}
	controller := ingest.NewController(source, func(context.Context) (func(context.Context) error, error) {
		return func(context.Context) error {
			t.Fatal("capture runner started without accessibility permission")
			return nil
		}, nil
	})
	if _, err := startCaptureOnLaunch(context.Background(), controller, false); err != nil {
		t.Fatalf("explicitly paused startup = %v", err)
	}
	if controller.Running() {
		t.Fatal("capture started after explicit pause")
	}
	if _, err := startCaptureOnLaunch(context.Background(), controller, true); err == nil {
		t.Fatal("startup capture ignored missing permission")
	}
	if controller.Running() {
		t.Fatal("capture started without permission")
	}
}

func TestStartupCaptureRequestsAccessibilityPromptOnceWhenEnabled(t *testing.T) {
	source := &startupCaptureSource{state: platform.PermissionRequired}
	controller := ingest.NewController(source, func(context.Context) (func(context.Context) error, error) {
		return func(context.Context) error { t.Fatal("capture runner started without permission"); return nil }, nil
	})
	for i := 0; i < 3; i++ {
		if _, err := startCaptureOnLaunch(context.Background(), controller, true); err == nil {
			t.Fatal("startup ignored missing Accessibility permission")
		}
	}
	if got := source.prompts.Load(); got != 1 {
		t.Fatalf("permission prompt count = %d, want one prompt while auto-checking", got)
	}
	if _, err := startCaptureOnLaunch(context.Background(), controller, false); err != nil {
		t.Fatalf("paused startup = %v", err)
	}
	if got := source.prompts.Load(); got != 1 {
		t.Fatalf("paused startup changed permission prompt count to %d", got)
	}
}

func TestStartupCaptureReturnsFreshPermissionStateWhenStartRacesTCCChange(t *testing.T) {
	source := &startupCaptureSource{sequence: []platform.CapabilityState{platform.Available, platform.PermissionRequired, platform.PermissionRequired}}
	controller := ingest.NewController(source, func(context.Context) (func(context.Context) error, error) {
		return func(context.Context) error { return nil }, nil
	})
	report, err := startCaptureOnLaunch(context.Background(), controller, true)
	if err == nil {
		t.Fatal("start should fail after Accessibility permission is revoked")
	}
	if report.State != platform.PermissionRequired {
		t.Fatalf("returned stale capability state %q, want permission_required", report.State)
	}
	if !captureSetupRequired(report, controller.Running()) {
		t.Fatal("permission race did not leave setup active for automatic recheck")
	}
}

func TestCapturePermissionStateOffersSettingsOnlyWhenAccessIsMissing(t *testing.T) {
	tests := []struct {
		name    string
		report  platform.CapabilityReport
		running bool
		err     error
		want    string
	}{
		{"missing permission", platform.CapabilityReport{State: platform.PermissionRequired}, false, nil, "permission_required"},
		{"capture running", platform.CapabilityReport{State: platform.Available}, true, nil, "capture_on"},
		{"start failed", platform.CapabilityReport{State: platform.Available}, false, errors.New("fixture"), "unavailable"},
		{"temporarily unavailable", platform.CapabilityReport{State: platform.TemporarilyUnresponsive}, false, nil, "unavailable"},
		{"paused", platform.CapabilityReport{}, false, nil, "paused"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := captureAvailabilityState(test.report, test.running, test.err); got != test.want {
				t.Fatalf("captureAvailabilityState() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPermissionSetupCopyUsesCurrentSettingsNameAndAutomaticRecheck(t *testing.T) {
	got := captureStartupUnavailableMessage(platform.CapabilityReport{State: platform.PermissionRequired}, nil)
	for _, want := range []string{"Accessibility", "Device Control & Data Access", "checking automatically"} {
		if !strings.Contains(got, want) {
			t.Errorf("permission setup message %q does not contain %q", got, want)
		}
	}
}

func TestCaptureRecoveryDelayIsCappedExponentialBackoffWithSixAttempts(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
	for attempt, delay := range want {
		got, ok := captureRecoveryDelay(attempt)
		if !ok || got != delay {
			t.Fatalf("attempt %d = %v, %v; want %v, true", attempt, got, ok, delay)
		}
	}
	for _, attempt := range []int{-1, len(want), 100} {
		if got, ok := captureRecoveryDelay(attempt); ok || got != 0 {
			t.Fatalf("attempt %d = %v, %v; want exhausted", attempt, got, ok)
		}
	}
}
