package preferences

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVoiceAndLoggingDefaultsUpgradeLegacyWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	legacy := []byte(`{"capture_enabled":false,"menu_bar_item_enabled":true,"claude_mcp_enabled":true,"included_apps":["com.example.work"]}`)
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["voice_notes"]) != `{"source":"both","locale":"en-US"}` || string(got["log_level"]) != `"info"` {
		t.Fatalf("new defaults missing from upgraded config: %s", data)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(legacy) {
		t.Fatalf("Load rewrote legacy settings: %q, %v", after, err)
	}
	if config.CaptureEnabled || !config.ClaudeMCPEnabled || len(config.IncludedApps) != 1 {
		t.Fatal("legacy consent/capture preferences lost")
	}
}

func TestStoreAcceptsVoiceAndLogSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	data := []byte(`{"capture_enabled":true,"menu_bar_item_enabled":true,"voice_notes":{"source":"system","locale":"nl-BE"},"log_level":"debug"}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err != nil {
		t.Fatalf("supported source/language/log level rejected: %v", err)
	}
}

func TestVoiceAndLogValidationRoundTrip(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{VoiceSourceBoth, VoiceSourceMicrophone, VoiceSourceSystem} {
		for _, locale := range []string{"en-US", "nl-BE", "fr-FR", "zh-Hant-TW", "ja-JP"} {
			for _, level := range []string{"debug", "info", "warn", "error", ""} {
				config := DefaultConfig()
				config.VoiceNotes = VoiceNotesConfig{Source: source, Locale: locale}
				config.LogLevel = level
				if err := store.Save(context.Background(), config); err != nil {
					t.Fatalf("valid %s/%s/%s: %v", source, locale, level, err)
				}
				got, err := store.Load(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if got.VoiceNotes != config.VoiceNotes || got.LogLevel == "" || (level != "" && got.LogLevel != level) {
					t.Fatalf("round trip: %+v", got)
				}
			}
		}
	}
	for _, config := range []Config{
		{VoiceNotes: VoiceNotesConfig{Source: "none", Locale: "en-US"}},
		{VoiceNotes: VoiceNotesConfig{Source: "both", Locale: "not a locale"}},
		{VoiceNotes: VoiceNotesConfig{Source: "both", Locale: "../en-US"}},
		{VoiceNotes: VoiceNotesConfig{Source: "both", Locale: "en-US\x00"}},
		{VoiceNotes: VoiceNotesConfig{Source: "both", Locale: string(make([]byte, 65))}},
		{LogLevel: "trace"}, {LogLevel: "INFO"}, {LogLevel: "debug\n"},
	} {
		if err := store.Save(context.Background(), config); err == nil {
			t.Fatalf("invalid config accepted: %+v", config)
		}
	}
	got, err := NormalizeVoiceNotes(VoiceNotesConfig{Source: "microphone", Locale: " en-us "})
	if err != nil || got.Locale != "en-US" {
		t.Fatalf("normalized language = %+v %v", got, err)
	}
}

func TestStoreUpdatePreservesLatestUnrelatedPreferencesAndOwnsSlices(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig()
	config.CaptureEnabled = false
	config.ClaudeMCPEnabled = true
	config.SafariPageCaptureEnabled = true
	config.ExcludedApps = []string{"com.example.private"}
	config.IncludedDomains = []string{"work.test"}
	if err := store.Save(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	var retained []string
	got, err := store.Update(context.Background(), func(latest Config) (Config, error) {
		retained = latest.ExcludedApps
		latest.VoiceNotes = VoiceNotesConfig{Source: VoiceSourceSystem, Locale: "nl-BE"}
		return latest, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	retained[0] = "com.example.changed"
	if got.CaptureEnabled || !got.ClaudeMCPEnabled || !got.SafariPageCaptureEnabled || got.ExcludedApps[0] != "com.example.private" || got.IncludedDomains[0] != "work.test" {
		t.Fatalf("unrelated settings changed or aliased: %+v", got)
	}
	got.ExcludedApps[0] = "com.example.returned"
	latest, err := store.Load(context.Background())
	if err != nil || latest.ExcludedApps[0] != "com.example.private" {
		t.Fatalf("saved settings aliased: %+v %v", latest, err)
	}
	sentinel := errors.New("declined")
	if _, err := store.Update(context.Background(), func(latest Config) (Config, error) { latest.LogLevel = "debug"; return latest, sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	latest, err = store.Load(context.Background())
	if err != nil || latest.LogLevel != "info" {
		t.Fatal("failed callback changed settings")
	}
}

func TestStoreUpdateSerializesAndRechecksCancellation(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := store.Update(context.Background(), func(c Config) (Config, error) { close(entered); <-release; c.LogLevel = "debug"; return c, nil })
		finished <- err
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if _, err := store.Update(ctx, func(c Config) (Config, error) { called = true; return c, nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled edit = %v, called=%v", err, called)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	got, err := store.Update(context.Background(), func(c Config) (Config, error) { c.VoiceNotes.Source = VoiceSourceMicrophone; return c, nil })
	if err != nil || got.LogLevel != "debug" || got.VoiceNotes.Source != VoiceSourceMicrophone {
		t.Fatalf("atomic merge = %+v %v", got, err)
	}
}
