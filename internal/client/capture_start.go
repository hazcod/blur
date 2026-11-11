package client

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/hazcod/blur/internal/ingest"
	"github.com/hazcod/blur/internal/platform"
)

var (
	errCapturePermissionRequired = errors.New("macos client: accessibility permission required")
	errCaptureUnavailable        = errors.New("macos client: capture unavailable")
)

// startCaptureOnLaunch honors the stored consent preference. A missing
// capability leaves capture stopped; macOS may show its own guidance once.
func startCaptureOnLaunch(ctx context.Context, controller *ingest.Controller, enabled bool) (platform.CapabilityReport, error) {
	if !enabled {
		return platform.CapabilityReport{}, nil
	}
	if controller == nil {
		return platform.CapabilityReport{}, ingest.ErrCaptureNotAvailable
	}
	report, err := controller.Capability(ctx)
	if err != nil {
		return report, err
	}
	if report.State != platform.Available {
		if report.State == platform.PermissionRequired {
			// Ask macOS to show its own Accessibility guidance once; Blur never
			// changes TCC state and continues rechecking capability separately.
			if err := controller.RequestPermission(ctx); err != nil {
				slog.Warn("macOS Accessibility guidance could not be opened", "result", "native_prompt_failed")
			}
		}
		return report, ingest.ErrCaptureNotAvailable
	}
	if err := controller.Start(ctx); err != nil {
		// Start repeats the capability check to close the time-of-check/time-of-
		// use window. Re-read after a failed start so the setup UI reflects the
		// latest TCC state instead of pinning a stale “available” report.
		if current, checkErr := controller.Capability(ctx); checkErr == nil {
			report = current
		}
		return report, err
	}
	return report, nil
}

func (d *desktop) startCaptureOnLaunch() {
	if d == nil || d.app == nil || d.capture == nil || !d.preferences.CaptureEnabled || d.capture.Running() || d.captureStarting {
		return
	}
	d.captureStarting = true
	d.setupRequired = true
	d.status = "Starting capture…"
	d.app.RequestUpdate()
	go func() {
		report, err := startCaptureOnLaunch(context.Background(), d.capture, true)
		d.app.Post(func() {
			d.captureStarting = false
			d.captureState = captureAvailabilityState(report, d.capture.Running(), err)
			if err == nil && d.capture.Running() {
				d.noteCaptureStarted()
				d.setupRequired = false
				d.stopPermissionWatch()
				d.status = "Capture is on for the currently focused app."
				d.compactQuickPanelAfterCapture()
			} else if err == nil {
				d.setupRequired = false
				d.captureState = "unavailable"
				d.status = "Capture stopped unexpectedly. Check Settings, then resume capture."
			} else {
				d.setupRequired = captureSetupRequired(report, d.capture.Running())
				d.status = captureStartupUnavailableMessage(report, err)
				if d.preferences.CaptureEnabled && report.State != platform.PermissionRequired {
					d.scheduleCaptureRecovery()
				}
			}
			if d.setupRequired {
				d.startPermissionWatch()
			}
			d.updateMenuBarCaptureState()
			if err == nil {
				d.loadToday()
			}
			d.app.RequestUpdate()
		})
	}()
}

const (
	captureRecoveryMaxAttempts = 6
	captureRecoveryMaxDelay    = 30 * time.Second
	// captureRecoveryStableRun is how long capture must run before an earlier
	// failure streak is forgotten.
	captureRecoveryStableRun = 60 * time.Second
)

// captureRecoveryDelay returns the backoff before the zero-based recovery
// attempt: 1s, 2s, 4s, 8s, 16s, capped at 30s. ok is false once the failure
// streak has used all captureRecoveryMaxAttempts.
func captureRecoveryDelay(attempt int) (time.Duration, bool) {
	if attempt < 0 || attempt >= captureRecoveryMaxAttempts {
		return 0, false
	}
	delay := time.Second << uint(attempt)
	if delay > captureRecoveryMaxDelay {
		delay = captureRecoveryMaxDelay
	}
	return delay, true
}

// noteCaptureStarted records a successful capture start from any path (launch,
// manual resume, settings restart) so the recovery streak can be reset once
// the worker has run for captureRecoveryStableRun. Every successful start must
// call it.
func (d *desktop) noteCaptureStarted() {
	if d != nil {
		d.captureStartedAt = time.Now()
	}
}

// scheduleCaptureRecovery retries an unexpected worker exit with capped
// exponential backoff; an explicit global pause or in-progress privacy action
// always suppresses recovery.
func (d *desktop) scheduleCaptureRecovery() {
	if d == nil || d.app == nil || d.capture == nil || !d.preferences.CaptureEnabled ||
		d.loading || d.captureStarting || d.capture.Running() || d.captureRecoveryScheduled || d.setupRequired {
		return
	}
	if !d.captureStartedAt.IsZero() && time.Since(d.captureStartedAt) >= captureRecoveryStableRun {
		d.captureRecoveryAttempts = 0
	}
	attempt := d.captureRecoveryAttempts
	delay, ok := captureRecoveryDelay(int(attempt))
	if !ok {
		d.status = "Capture keeps stopping. Blur stopped retrying to protect the app; check its log before resuming."
		d.app.RequestUpdate()
		return
	}
	d.captureRecoveryAttempts++
	d.captureRecoveryScheduled = true
	slog.Warn("capture worker recovery scheduled", "delay_seconds", int(delay.Seconds()), "attempt", int(d.captureRecoveryAttempts))
	d.app.TimeoutFunc(delay, func() {
		d.captureRecoveryScheduled = false
		if !d.preferences.CaptureEnabled || d.loading || d.captureStarting || d.capture.Running() {
			return
		}
		d.startCaptureOnLaunch()
	})
}

func captureSetupRequired(report platform.CapabilityReport, running bool) bool {
	return !running && report.State == platform.PermissionRequired
}

func desktopWindowOnActivation(dashboardOpen bool) string {
	if dashboardOpen {
		return "main"
	}
	return "quick"
}

func reopenDesktopOnActivation(dashboardOpen bool, show func(string) error, recheck func()) error {
	var err error
	if show != nil {
		err = show(desktopWindowOnActivation(dashboardOpen))
	}
	if recheck != nil {
		recheck()
	}
	return err
}

// Recheck permission when macOS activates an app (for example, when the user
// returns from System Settings). Polling is used only if this event source is
// unavailable, and stops as soon as capture starts or setup closes.
func (d *desktop) startPermissionWatch() {
	if d == nil || d.permissionWatchActive || !d.setupRequired {
		return
	}
	stopWatch := make(chan struct{})
	d.permissionWatchStop = stopWatch
	d.permissionWatchActive = true
	// Keep a low-frequency safety net even when activation notifications are
	// installed: successful subscription does not guarantee every transition
	// will be delivered. This watcher exists only while setup is unresolved.
	interval := permissionWatchInterval(d.permissionActivationAvailable)
	slog.Info("Accessibility permission watcher started", "interval_seconds", int(interval.Seconds()), "activation_notifications", d.permissionActivationAvailable)
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		watchPermissionRechecks(nil, ticker.C, stopWatch, d.captureSetupDone, func() {
			d.app.Post(func() {
				if d.setupRequired && !d.captureStarting {
					d.startCaptureOnLaunch()
				}
			})
		})
	}()
}

func permissionWatchInterval(activationAvailable bool) time.Duration {
	if activationAvailable {
		return 10 * time.Second
	}
	return 2 * time.Second
}

func (d *desktop) watchDesktopReactivation(done <-chan struct{}) {
	if d == nil || d.app == nil {
		return
	}
	events, stop, err := watchDesktopActivation()
	if err != nil {
		slog.Warn("self activation notifications unavailable", "result", "periodic_setup_checks_only")
		return
	}
	d.permissionActivationAvailable = true
	go func() {
		defer stop()
		for {
			select {
			case <-done:
				return
			case _, ok := <-events:
				if !ok {
					return
				}
				d.app.Post(func() {
					if err := reopenDesktopOnActivation(d.dashboardOpen, func(id string) error {
						window := d.app.FindWindow(id)
						if window == nil {
							return nil
						}
						return window.Show()
					}, func() {
						if d.setupRequired && !d.captureStarting {
							d.startCaptureOnLaunch()
						}
					}); err != nil {
						slog.Warn("Blur window could not be shown on activation", "result", "failed")
					}
				})
			}
		}
	}()
}

func watchPermissionRechecks(events <-chan struct{}, ticks <-chan time.Time, stop, done <-chan struct{}, onRecheck func()) {
	for {
		select {
		case <-done:
			return
		case <-stop:
			return
		case _, ok := <-events:
			if !ok {
				return
			}
			if onRecheck != nil {
				onRecheck()
			}
		case <-ticks:
			if onRecheck != nil {
				onRecheck()
			}
		}
	}
}

func (d *desktop) stopPermissionWatch() {
	if d == nil || d.permissionWatchStop == nil {
		return
	}
	close(d.permissionWatchStop)
	d.permissionWatchStop = nil
	d.permissionWatchActive = false
	slog.Info("Accessibility permission watcher stopped")
}

func captureAvailabilityState(report platform.CapabilityReport, running bool, err error) string {
	if running {
		return "capture_on"
	}
	if report.State == platform.PermissionRequired {
		return "permission_required"
	}
	if err != nil || report.State != platform.Available && report.State != "" {
		return "unavailable"
	}
	return "paused"
}

func captureStartupUnavailableMessage(report platform.CapabilityReport, err error) string {
	if report.State == platform.PermissionRequired {
		return "macOS still reports that this Blur build lacks Accessibility / Device Control & Data Access. Blur keeps checking automatically while this window is open."
	}
	if err != nil {
		return "Capture is enabled but unavailable. Check Accessibility availability, then retry from Settings."
	}
	return "Capture is paused."
}

// compactQuickPanelAfterCapture resizes the quick panel once capture is
// running. The native window may not exist yet (it is created asynchronously
// when first shown), so a missing handle is expected and only logged at debug.
func (d *desktop) compactQuickPanelAfterCapture() {
	if d == nil || d.app == nil {
		return
	}
	window := d.app.FindWindow("quick")
	if window == nil || window.PlatformWindow().NativeHandle() == 0 {
		slog.Debug("quick capture panel not created yet; skipping compaction", "result", "deferred")
		return
	}
	if err := resizeDesktopQuickPanel(d.app, float64(d.quickPanelDesiredHeight())); err != nil {
		slog.Warn("quick capture panel could not be compacted", "result", "resize_failed")
	}
}
