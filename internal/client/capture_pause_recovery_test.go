package client

import (
	"context"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/ingest"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/preferences"
)

type pauseRecoveryApp struct {
	voiceMeterApp
	posted chan func()
}

func (a *pauseRecoveryApp) Post(fn func()) { a.posted <- fn }

func pausedRecoveryFixture(t *testing.T) (*desktop, *pauseRecoveryApp) {
	t.Helper()
	app := &pauseRecoveryApp{posted: make(chan func(), 8)}
	controller := ingest.NewController(&startupCaptureSource{state: platform.TemporarilyUnresponsive}, func(context.Context) (func(context.Context) error, error) {
		t.Error("unavailable synthetic capture should not create a worker")
		return nil, nil
	})
	d := &desktop{app: app, capture: controller, preferences: preferences.DefaultConfig(), capturePauseUnsaved: true}
	return d, app
}

func awaitUnexpectedCaptureStart(t *testing.T, app *pauseRecoveryApp) {
	t.Helper()
	select {
	case <-app.posted: // Retire the synthetic startup worker without native UI.
	case <-time.After(3 * time.Second):
		t.Fatal("unexpected startup worker did not finish")
	}
}

func TestUnsavedPauseSuppressesAutomaticStartAndRecovery(t *testing.T) {
	d, app := pausedRecoveryFixture(t)
	d.startCaptureOnLaunch()
	if d.captureStarting {
		t.Error("automatic startup ignored the unsaved session pause")
		awaitUnexpectedCaptureStart(t, app)
	}
	d, app = pausedRecoveryFixture(t)
	d.scheduleCaptureRecovery()
	if d.captureRecoveryScheduled || len(app.timers) != 0 {
		t.Error("recovery admission ignored the unsaved session pause")
	}
}

func TestQueuedCaptureRecoveryHonorsLaterUnsavedPause(t *testing.T) {
	d, app := pausedRecoveryFixture(t)
	d.capturePauseUnsaved = false
	d.scheduleCaptureRecovery()
	if len(app.timers) != 1 {
		t.Fatal("fixture did not queue an earlier recovery")
	}
	d.capturePauseUnsaved = true
	app.timers[0]()
	if d.captureStarting {
		t.Error("queued recovery restarted a later unsaved session pause")
		awaitUnexpectedCaptureStart(t, app)
	}
	if d.captureRecoveryScheduled {
		t.Error("skipped recovery left its scheduling flag active")
	}
}
