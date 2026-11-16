package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestUnconfiguredLocalAIUsesPlatformDefaultsWithoutWriting(t *testing.T) {
	want := LocalAISettings{}
	if runtime.GOOS == "darwin" {
		want = LocalAISettings{Enabled: true, Provider: "apple-foundation-models", Model: "system-language-model", AutomaticLibrary: true, BackgroundSummaries: true}
	}
	for _, test := range []struct {
		name string
		data string
	}{
		{name: "missing file"},
		{name: "empty object", data: `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "local-ai.json")
			if test.data != "" {
				if err := writePrivateSettingsFixture(path, []byte(test.data)); err != nil {
					t.Fatal(err)
				}
			}
			got, err := readLocalAISettings(path)
			if err != nil || got != want {
				t.Fatalf("unconfigured AI = %#v, %v; want %#v", got, err, want)
			}
			if test.data == "" {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("default load created a settings file: %v", err)
				}
			} else if data, err := os.ReadFile(path); err != nil || string(data) != test.data {
				t.Fatalf("default load changed existing settings: %q, %v", data, err)
			}
		})
	}
}

func TestLocalAIBackgroundReconfigurationPreservesOtherExplicitPause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-ai.json")
	controller := &localAIController{path: path, settings: LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}}
	if err := controller.SetBackgroundSummaries(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	loaded, err := readLocalAISettings(path)
	if err != nil || !loaded.BackgroundSummaries || loaded.AutomaticLibrary {
		t.Fatal("enabling recaps reset the Library pause", err)
	}
	if err := controller.SetAutomaticLibrary(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := controller.SetBackgroundSummaries(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	loaded, err = readLocalAISettings(path)
	if err != nil || loaded.BackgroundSummaries || !loaded.AutomaticLibrary {
		t.Fatal("pausing recaps reset Automatic Library", err)
	}
}

func TestLocalAIWriterPreservesExplicitBackgroundOffFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-ai.json")
	settings := LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}
	if err := writeLocalAISettings(path, settings); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"automatic_library", "background_summaries"} {
		if string(fields[field]) != "false" {
			t.Fatalf("explicit Off for %s was omitted", field)
		}
	}
	loaded, err := readLocalAISettings(path)
	if err != nil || loaded != settings {
		t.Fatal("explicit background Off changed after reload", err)
	}
}

func TestLocalAIDefaultsPreserveSavedOffAndProviderChoices(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
		want LocalAISettings
	}{
		{name: "explicit off", data: `{"enabled":false}`},
		{name: "Apple legacy Library off", data: `{"enabled":true,"provider":"apple-foundation-models","model":"system-language-model"}`, want: LocalAISettings{Enabled: true, Provider: "apple-foundation-models", Model: "system-language-model"}},
		{name: "Apple explicit Library off", data: `{"enabled":true,"automatic_library":false,"provider":"apple-foundation-models","model":"system-language-model"}`, want: LocalAISettings{Enabled: true, Provider: "apple-foundation-models", Model: "system-language-model"}},
		{name: "saved Ollama", data: `{"enabled":true,"endpoint":"http://127.0.0.1:11434","model":"fixture"}`, want: LocalAISettings{Enabled: true, Endpoint: "http://127.0.0.1:11434", Model: "fixture"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "local-ai.json")
			if err := writePrivateSettingsFixture(path, []byte(test.data)); err != nil {
				t.Fatal(err)
			}
			got, err := readLocalAISettings(path)
			if err != nil || got != test.want {
				t.Fatalf("saved AI = %#v, %v; want %#v", got, err, test.want)
			}
		})
	}
}

func TestLocalAIDefaultsDoNotRepairInvalidSettings(t *testing.T) {
	for _, data := range []string{`null`, `{"enabled":true,"provider":"apple-foundation-models"}`, `{"enabled":true,"extra":true}`} {
		path := filepath.Join(t.TempDir(), "local-ai.json")
		if err := writePrivateSettingsFixture(path, []byte(data)); err != nil {
			t.Fatal(err)
		}
		if got, err := readLocalAISettings(path); !errors.Is(err, errLocalAISettings) || got != (LocalAISettings{}) {
			t.Fatalf("invalid AI %s = %#v, %v; want empty settings and repair error", data, got, err)
		}
	}
}
