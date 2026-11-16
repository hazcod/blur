package client

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/preferences"
)

type safariToggleCapture interface {
	Running() bool
	Pause(context.Context) error
	Start(context.Context) error
}
type safariTogglePreferences interface {
	ConfigureApplicationPreferences(context.Context, preferences.Config) error
	ApplicationPreferences(context.Context) (preferences.Config, error)
}
type safariToggleRecovery struct {
	Preferences                             preferences.Config
	Err, RollbackErr, RestartErr, ReloadErr error
	Running, Started                        bool
}

// Capture may resume only after the requested setting, or its restoration,
// has been saved successfully. A failed restoration is never permission to
// restart a worker against an unknown configuration.
func applySafariCaptureToggle(capture safariToggleCapture, settings safariTogglePreferences, previous, next preferences.Config, wasRunning bool) (r safariToggleRecovery) {
	r.Preferences = previous
	configure := func(config preferences.Config, timeout time.Duration) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return settings.ConfigureApplicationPreferences(ctx, config)
	}
	pause := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		return capture.Pause(ctx)
	}
	defer func() { r.Running = capture.Running() }()
	if wasRunning {
		if r.Err = pause(); r.Err != nil {
			return r
		}
	}
	r.Err = configure(next, 8*time.Second)
	if r.Err == nil {
		r.Preferences = next
		if wasRunning {
			// The controller owns the lifetime of a started worker. A bounded
			// settings-operation context must not cancel it when this returns.
			r.Err = capture.Start(context.Background())
			r.Started = r.Err == nil
		}
	}
	if r.Err == nil {
		return r
	}
	r.RollbackErr = configure(previous, 3*time.Second)
	if r.RollbackErr == nil {
		r.Preferences = previous
		if wasRunning && !capture.Running() {
			r.RestartErr = capture.Start(context.Background())
			r.Started = r.RestartErr == nil
		}
	} else if capture.Running() {
		// Fail closed even if another controller path managed to start while
		// the failed settings operation was completing.
		_ = pause()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	actual, err := settings.ApplicationPreferences(ctx)
	cancel()
	r.ReloadErr = err
	if err == nil {
		r.Preferences = actual
	}
	return r
}

func safariToggleRecoveryMessage(r safariToggleRecovery, next preferences.Config) string {
	if r.Err == nil {
		if next.SafariPageCaptureEnabled {
			return "Safari page capture enabled for active, non-private tabs."
		}
		return "Safari page capture disabled."
	}
	if r.RollbackErr != nil {
		message := "Safari capture could not be changed, and the previous setting could not be restored. "
		if r.Running {
			message += "Capture could not be paused. "
		} else {
			message += "Capture remains paused. "
		}
		if r.ReloadErr != nil {
			message += "The saved setting could not be verified. "
		}
		return message + "Review Capture settings before resuming."
	}
	if r.RestartErr != nil {
		return "The previous Safari setting was restored, but capture could not restart. It remains paused; review Capture settings before resuming."
	}
	if r.Started {
		return "Safari capture could not be changed. The previous setting was restored and capture resumed."
	}
	if r.Running {
		return "Safari capture could not be changed because capture did not pause cleanly. Review Capture settings before retrying."
	}
	return "Safari capture could not be changed. Capture remains paused; review Capture settings before resuming."
}
