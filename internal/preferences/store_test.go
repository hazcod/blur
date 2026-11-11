package preferences

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestStoreLoadsAndPersistsPrivacyExclusions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	if err := os.WriteFile(path, []byte(`{"capture_enabled":true,"menu_bar_item_enabled":true,"excluded_apps":["com.example.private"],"excluded_domains":["private.test"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() rejected supported privacy exclusions: %v", err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"excluded_apps":    `["com.example.private"]`,
		"excluded_domains": `["private.test"]`,
	} {
		if got := string(roundTrip[key]); got != want {
			t.Fatalf("%s round-trip = %s, want %s", key, got, want)
		}
	}
}

func TestStoreNormalizesAndRejectsUnsafePrivacyExclusions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig()
	config.ExcludedApps = []string{" COM.Example.Private "}
	config.ExcludedDomains = []string{"Private.Test."}
	if err := store.Save(context.Background(), config); err != nil {
		t.Fatalf("Save() valid exclusions: %v", err)
	}
	got, err := store.Load(context.Background())
	if err != nil || !reflect.DeepEqual(got.ExcludedApps, []string{"com.example.private"}) || !reflect.DeepEqual(got.ExcludedDomains, []string{"private.test"}) {
		t.Fatalf("normalized exclusions = %+v, %v", got, err)
	}

	for _, invalid := range []Config{
		{CaptureEnabled: true, MenuBarItemEnabled: true, ExcludedApps: []string{"com.example/private"}},
		{CaptureEnabled: true, MenuBarItemEnabled: true, ExcludedDomains: []string{"https://private.test"}},
		{CaptureEnabled: true, MenuBarItemEnabled: true, ExcludedDomains: []string{"private..test"}},
		{CaptureEnabled: true, MenuBarItemEnabled: true, ExcludedApps: make([]string, maxExclusionEntries+1)},
	} {
		if err := store.Save(context.Background(), invalid); !errors.Is(err, ErrSettingsUnavailable) {
			t.Fatalf("Save() invalid exclusions %+v error = %v, want ErrSettingsUnavailable", invalid, err)
		}
	}
}

func TestFirstRunDefaultsCaptureAndMenuBarOnWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	want := Config{CaptureEnabled: true, MenuBarItemEnabled: true, SafariPageCaptureEnabled: false}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("first-run config = %+v, %v; want %+v", got, err, want)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default load created settings file: %v", err)
	}
}

func TestStorePersistsExplicitCapturePauseAndMenuBarChoice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{CaptureEnabled: false, MenuBarItemEnabled: false, SafariPageCaptureEnabled: true}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted config = %+v, %v; want %+v", got, err, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestClaudeMCPConsentDefaultsOffAndPersistsExplicitOptIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := store.Load(context.Background())
	if err != nil || defaults.ClaudeMCPEnabled {
		t.Fatalf("Claude MCP first-run consent = %+v, %v; want off", defaults, err)
	}
	want := defaults
	want.ClaudeMCPEnabled = true
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Claude MCP consent did not round-trip: got=%+v err=%v want=%+v", got, err, want)
	}
}

func TestStoreLoadsLegacyPreferencesWithSafariCaptureDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	if err := os.WriteFile(path, []byte(`{"capture_enabled":true,"menu_bar_item_enabled":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	want := Config{CaptureEnabled: true, MenuBarItemEnabled: false, SafariPageCaptureEnabled: false}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy preferences = %+v, %v; want %+v", got, err, want)
	}
}

func TestStoreRejectsMalformedOrUnsafeSettings(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "preferences.json")
	if err := os.WriteFile(path, []byte(`{"capture_enabled":true,"menu_bar_item_enabled":true,"extra":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); !errors.Is(err, ErrSettingsUnavailable) {
		t.Fatalf("unknown setting error = %v, want ErrSettingsUnavailable", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte(`{"capture_enabled":true,"menu_bar_item_enabled":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); !errors.Is(err, ErrSettingsUnavailable) {
		t.Fatalf("symlink settings error = %v, want ErrSettingsUnavailable", err)
	}
}

func TestStoreHonorsCanceledContext(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load canceled error = %v", err)
	}
	if err := store.Save(ctx, DefaultConfig()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Save canceled error = %v", err)
	}
}

func TestStorePersistsQuickAskHotkeyAndDropsUnknownShortcuts(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig()
	if config.QuickAskHotkey != "" {
		t.Fatalf("default hotkey = %q, want off", config.QuickAskHotkey)
	}
	config.QuickAskHotkey = "alt+space"
	if err := store.Save(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil || loaded.QuickAskHotkey != "alt+space" {
		t.Fatalf("loaded hotkey = %q err=%v", loaded.QuickAskHotkey, err)
	}
	config.QuickAskHotkey = "hyper+q"
	if err := store.Save(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load(context.Background())
	if err != nil || loaded.QuickAskHotkey != "" {
		t.Fatalf("unknown hotkey loaded as %q err=%v, want off", loaded.QuickAskHotkey, err)
	}
}
