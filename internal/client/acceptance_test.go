package client

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hazcod/blur/internal/acceptance"
	"github.com/hazcod/blur/internal/preferences"
)

func TestAcceptancePreferencesStartPausedAndPreserveFixtureChanges(t *testing.T) {
	config := acceptance.Config{Root: t.TempDir(), Domain: "blur-fixture.localhost", AppID: "com.hazcod.blur.acceptance.fixture"}
	ctx := context.Background()
	if err := prepareAcceptancePreferences(ctx, config); err != nil {
		t.Fatal(err)
	}
	store, err := preferences.NewStore(filepath.Join(config.DataDirectory(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	prefs, err := store.Load(ctx)
	if err != nil || prefs.CaptureEnabled || prefs.MenuBarItemEnabled || prefs.QuickAskHotkey != "" || prefs.SafariPageCaptureEnabled || prefs.ClaudeMCPEnabled || !prefs.CaptureDefaultDisabled || !reflect.DeepEqual(prefs.IncludedApps, []string{config.AppID}) || !reflect.DeepEqual(prefs.IncludedDomains, []string{config.Domain}) {
		t.Fatal("fixture startup preferences were not isolated and paused", prefs, err)
	}
	prefs.CaptureEnabled = true
	if err := store.Save(ctx, prefs); err != nil {
		t.Fatal(err)
	}
	if err := prepareAcceptancePreferences(ctx, config); err != nil {
		t.Fatal(err)
	}
	again, err := store.Load(ctx)
	if err != nil || !reflect.DeepEqual(prefs, again) {
		t.Fatal("relaunch replaced fixture preferences", again, err)
	}
	info, err := os.Stat(config.DataDirectory())
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("fixture data is not private", err)
	}
}
