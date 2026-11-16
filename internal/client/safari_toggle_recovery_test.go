package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/ingest"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/preferences"
)

func TestSafariToggleFailedRestoreStaysPausedAndShowsFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := preferences.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	config := preferences.DefaultConfig()
	config.SafariPageCaptureEnabled = true
	if err := store.Save(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	var starts atomic.Int32
	controller := ingest.NewController(&startupCaptureSource{state: platform.Available}, func(context.Context) (func(context.Context) error, error) {
		starts.Add(1)
		return func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, nil
	})
	t.Cleanup(func() { _ = controller.Pause(context.Background()) })
	if err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 8)}
	d := &desktop{app: app, client: NewClient(&testData{}).WithPreferences(store), capture: controller, preferences: config}
	d.toggleSafariPageCapture()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for d.loading {
		select {
		case publish := <-app.posted:
			publish()
		case <-deadline.C:
			t.Fatal("Safari settings operation did not finish")
		}
	}
	if controller.Running() || starts.Load() != 1 || !strings.Contains(d.status, "could not be restored") || strings.Contains(d.status, "was restored") {
		t.Fatalf("failed rollback restarted capture or falsely claimed restore: running=%t starts=%d status=%q", controller.Running(), starts.Load(), d.status)
	}
}

type safariRecoverySettings struct {
	actual       preferences.Config
	errors       []error
	loads, saves int
	loadErr      error
}

func (s *safariRecoverySettings) ConfigureApplicationPreferences(_ context.Context, config preferences.Config) error {
	at := s.saves
	s.saves++
	if at < len(s.errors) && s.errors[at] != nil {
		return s.errors[at]
	}
	s.actual = config
	return nil
}
func (s *safariRecoverySettings) ApplicationPreferences(context.Context) (preferences.Config, error) {
	s.loads++
	return s.actual, s.loadErr
}

type safariRecoveryCapture struct {
	running        bool
	startErrors    []error
	pauseErr       error
	starts, pauses int
}

func (c *safariRecoveryCapture) Running() bool { return c.running }
func (c *safariRecoveryCapture) Pause(context.Context) error {
	c.pauses++
	if c.pauseErr == nil {
		c.running = false
	}
	return c.pauseErr
}
func (c *safariRecoveryCapture) Start(context.Context) error {
	at := c.starts
	c.starts++
	if at < len(c.startErrors) && c.startErrors[at] != nil {
		return c.startErrors[at]
	}
	c.running = true
	return nil
}

func TestSafariToggleRecoveryReportsActualSettingsAndRestartFailures(t *testing.T) {
	failure := errors.New("synthetic settings/controller failure")
	for _, tc := range []struct {
		name                     string
		running                  bool
		saves, starts            []error
		pauseErr                 error
		wantStarts, wantSaves    int
		wantRunning, wantEnabled bool
		copy                     string
	}{
		{"failed configuration and rollback", true, []error{failure, failure}, nil, nil, 0, 2, false, true, "could not be restored"},
		{"saved new setting but failed restart and rollback", true, []error{nil, failure}, []error{failure}, nil, 1, 2, false, false, "could not be restored"},
		{"restored but restart failed", true, nil, []error{failure, failure}, nil, 2, 2, false, true, "capture could not restart"},
		{"restored and resumed", true, nil, []error{failure, nil}, nil, 2, 2, true, true, "capture resumed"},
		{"pause failed", true, nil, nil, failure, 0, 0, true, true, "did not pause cleanly"},
		{"successful running toggle", true, nil, nil, nil, 1, 1, true, false, "disabled"},
		{"successful paused toggle", false, nil, nil, nil, 0, 1, false, false, "disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := preferences.DefaultConfig()
			previous.SafariPageCaptureEnabled = true
			next := previous
			next.SafariPageCaptureEnabled = false
			settings := &safariRecoverySettings{actual: previous, errors: tc.saves}
			capture := &safariRecoveryCapture{running: tc.running, startErrors: tc.starts, pauseErr: tc.pauseErr}
			result := applySafariCaptureToggle(capture, settings, previous, next, tc.running)
			copy := safariToggleRecoveryMessage(result, next)
			if capture.starts != tc.wantStarts || settings.saves != tc.wantSaves || result.Running != tc.wantRunning || result.Preferences.SafariPageCaptureEnabled != tc.wantEnabled || !strings.Contains(copy, tc.copy) {
				t.Fatalf("incorrect recovery: starts=%d saves=%d running=%t safari=%t status=%q", capture.starts, settings.saves, result.Running, result.Preferences.SafariPageCaptureEnabled, copy)
			}
			if settings.saves == 2 && settings.loads != 1 {
				t.Fatal("failed change did not reload actual saved preferences")
			}
		})
	}
}
