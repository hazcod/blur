package client

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/ingest"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/preferences"
)

func TestRestorePreferencesDoesNotBlockUIBehindCaptureAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := preferences.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	config := preferences.DefaultConfig()
	if err := store.Save(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	// Bootstrap attaches preferences (including the saved log level) before
	// capture can acquire its policy lease. Keep that real startup order so
	// this fixture blocks the action under test, not client construction.
	client := NewClient(&testData{}).WithPreferences(store)
	t.Cleanup(client.Close)
	entered, release, unlocked := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	go func() {
		_ = store.WithConfig(context.Background(), func(preferences.Config) error { close(entered); <-release; return nil })
		close(unlocked)
	}()
	<-entered
	// Force the eventual persistence failure so this test never reaches native
	// menu/hotkey installation, whether exercising the old or fixed UI path.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 2)}
	d := &desktop{app: app, client: client, preferences: config}
	returned := make(chan struct{})
	go func() { d.restoreDefaultPreferences(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Error("restoring preferences blocked the UI while capture held its admission lease")
		unblock()
		<-returned
		<-unlocked
		return
	}
	if !d.loading {
		t.Fatal("preference save did not leave a pending UI receipt")
	}
	unblock()
	<-unlocked
	app.finish(t)
	if d.loading || d.status != "Default preferences could not be saved." {
		t.Fatal("failed preference save did not publish its error and restore controls")
	}
}

func TestPauseCaptureCancelsAcquisitionBeforeSavingPreferences(t *testing.T) {
	store, err := preferences.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := preferences.DefaultConfig()
	if err := store.Save(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	// Bootstrap attaches preferences (including the saved log level) before
	// capture can acquire its policy lease. Keep that real startup order so
	// this fixture blocks the action under test, not client construction.
	client := NewClient(&testData{}).WithPreferences(store)
	t.Cleanup(client.Close)
	entered, canceled := make(chan struct{}), make(chan struct{})
	controller := ingest.NewController(&startupCaptureSource{state: platform.Available}, func(context.Context) (func(context.Context) error, error) {
		return func(ctx context.Context) error {
			return store.WithConfig(ctx, func(preferences.Config) error {
				close(entered)
				<-ctx.Done()
				close(canceled)
				return ctx.Err()
			})
		}, nil
	})
	t.Cleanup(func() { _ = controller.Pause(context.Background()) })
	if err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-entered
	app := &meetingOperationTestApp{posted: make(chan func(), 8)}
	d := &desktop{app: app, client: client, capture: controller, preferences: config}
	d.toggleCapture()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Error("pause tried to save settings before cancelling an acquisition holding the preference lease")
		_ = controller.Pause(context.Background())
	}
	select {
	case <-app.posted: // Operation worker has completed persistence; no native UI.
	case <-time.After(3 * time.Second):
		t.Fatal("pause did not finish its persistence operation")
	}
	saved, err := store.Load(context.Background())
	if err != nil || saved.CaptureEnabled || controller.Running() {
		t.Fatalf("pause did not persist disabled capture: config=%+v error=%v", saved, err)
	}
}

func TestFailedPauseSaveStaysPausedAndCanRetryPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := preferences.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	config := preferences.DefaultConfig()
	if err := store.Save(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	// Bootstrap attaches preferences (including the saved log level) before
	// capture can acquire its policy lease. Keep that real startup order so
	// this fixture blocks the action under test, not client construction.
	client := NewClient(&testData{}).WithPreferences(store)
	t.Cleanup(client.Close)
	controller := ingest.NewController(&startupCaptureSource{state: platform.Available}, func(context.Context) (func(context.Context) error, error) {
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
	d := &desktop{app: app, client: client, capture: controller, preferences: config}
	d.toggleCapture()
	app.finish(t)
	if controller.Running() || !d.capturePauseUnsaved || d.captureLightState() != captureLightPaused || d.status != capturePauseUnsavedCopy || !d.preferences.CaptureEnabled {
		t.Fatal("failed disk save hid the current pause or claimed the preference was saved")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	d.saveCapturePause()
	app.finish(t)
	saved, err := store.Load(context.Background())
	if err != nil || saved.CaptureEnabled || d.capturePauseUnsaved || controller.Running() || d.loading {
		t.Fatalf("pause retry did not persist without restarting capture: config=%+v error=%v", saved, err)
	}
}
