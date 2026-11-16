package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/applog"
	"github.com/hazcod/blur/internal/preferences"
)

func logSettingsClient(t *testing.T) (*Client, *preferences.Store, string) {
	t.Helper()
	previous := applog.CurrentLevel()
	t.Cleanup(func() { _ = applog.SetLevel(previous) })
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := preferences.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), preferences.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	c := NewClient(&testData{}).WithPreferences(store)
	t.Cleanup(c.Close)
	return c, store, path
}

func TestLogSettingsSaveAppliesAfterPersistenceAndReloadsOnAttach(t *testing.T) {
	c, store, _ := logSettingsClient(t)
	latest := preferences.DefaultConfig()
	latest.CaptureEnabled = false
	latest.ExcludedDomains = []string{"excluded.example"}
	if err := store.Save(context.Background(), latest); err != nil {
		t.Fatal(err)
	}
	saved, err := c.ConfigureLogLevel(context.Background(), "debug")
	if err != nil || saved.LogLevel != "debug" || saved.CaptureEnabled || len(saved.ExcludedDomains) != 1 || applog.CurrentLevel() != "debug" {
		t.Fatal("log edit lost current preferences or did not apply", err)
	}
	if err := applog.SetLevel("error"); err != nil {
		t.Fatal(err)
	}
	reopened := NewClient(&testData{}).WithPreferences(store)
	defer reopened.Close()
	if applog.CurrentLevel() != "debug" {
		t.Fatal("saved level was not restored when preferences were attached")
	}
}

func TestLogSettingsFailureNeverChangesActiveLevel(t *testing.T) {
	for _, operation := range []string{"invalid", "canceled", "unwritable", "closed"} {
		t.Run(operation, func(t *testing.T) {
			c, _, path := logSettingsClient(t)
			if err := applog.SetLevel("warn"); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			level := "debug"
			switch operation {
			case "invalid":
				level = "private-prompt-text"
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "unwritable":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "closed":
				c.Close()
			}
			_, err := c.ConfigureLogLevel(ctx, level)
			if err == nil || applog.CurrentLevel() != "warn" || strings.Contains(err.Error(), level) {
				t.Fatal("failed save changed log level or exposed request text")
			}
		})
	}
}

func TestLogSettingsFullPreferenceSaveAppliesOnlySuccessfulLevel(t *testing.T) {
	c, _, _ := logSettingsClient(t)
	next := preferences.DefaultConfig()
	next.LogLevel = "error"
	if err := c.ConfigureApplicationPreferences(context.Background(), next); err != nil || applog.CurrentLevel() != "error" {
		t.Fatal("full preference save did not apply logging", err)
	}
	next.LogLevel = "debug"
	next.ExcludedApps = []string{"invalid/app"}
	if err := c.ConfigureApplicationPreferences(context.Background(), next); err == nil || applog.CurrentLevel() != "error" {
		t.Fatal("failed full preference save changed logging")
	}
}

func TestLogSettingsSaveKeepsUIResponsiveAndMergesOnlyItsReceipt(t *testing.T) {
	c, store, _ := logSettingsClient(t)
	entered, release, unlocked := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	go func() {
		_ = store.WithConfig(context.Background(), func(preferences.Config) error { close(entered); <-release; return nil })
		close(unlocked)
	}()
	<-entered
	app := &meetingOperationTestApp{posted: make(chan func(), 8)}
	d := &desktop{app: app, client: c, preferences: preferences.DefaultConfig()}
	returned := make(chan struct{})
	go func() { d.saveLogLevel("debug"); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		unblock()
		<-returned
		t.Fatal("log save blocked UI on capture's preference admission")
	}
	if !d.loading || !d.logSettingsUI.state.Peek().Saving || applog.CurrentLevel() != "info" {
		t.Fatal("pending save applied early or did not show saving")
	}
	// The receipt must never restore a whole stale UI preference snapshot.
	d.preferences.MenuBarItemEnabled = false
	unblock()
	<-unlocked
	app.finish(t)
	if d.loading || d.logSettingsUI.state.Peek().Saving || d.preferences.MenuBarItemEnabled || d.preferences.LogLevel != "debug" || applog.CurrentLevel() != "debug" {
		t.Fatal("save receipt overwrote another UI setting or failed to apply")
	}
}

func TestLogSettingsViewDoesNotReadPreferencesOrWriteGlobalState(t *testing.T) {
	c, _, path := logSettingsClient(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	d := &desktop{client: c, preferences: preferences.DefaultConfig()}
	_ = d.logSettingsView()
	if applog.CurrentLevel() != "info" || d.loading || d.logSettingsUI.state.Peek().Status != "" {
		t.Fatal("rendering log settings performed persistence or changed runtime state")
	}
}

func TestLogSettingsSaveFailureHasTruthfulUIReceipt(t *testing.T) {
	c, _, path := logSettingsClient(t)
	if _, err := c.ConfigureLogLevel(context.Background(), "warn"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 8)}
	d := &desktop{app: app, client: c, preferences: preferences.Config{LogLevel: "warn"}}
	d.saveLogLevel("debug")
	app.finish(t)
	state := d.logSettingsUI.state.Peek()
	if d.loading || state.Saving || !strings.Contains(state.Status, "could not be saved") || d.preferences.LogLevel != "warn" || applog.CurrentLevel() != "warn" {
		t.Fatal("failed save falsely acknowledged a new level")
	}
}
