package client

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hazcod/blur/internal/preferences"
)

type testMCPControl struct {
	states []bool
	active bool
}

func (m *testMCPControl) SetEnabled(_ context.Context, enabled bool) error {
	m.states = append(m.states, enabled)
	m.active = enabled
	return nil
}
func (m *testMCPControl) Enabled() bool { return m.active }
func (m *testMCPControl) Close() error  { m.active = false; return nil }

func TestLocalAppDefaultsCaptureOnAndPersistsExplicitPause(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "Blur")
	secrets := &bootstrapSecrets{values: make(map[string][]byte)}
	app, err := openLocalAppAt(context.Background(), directory, secrets, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	got, err := app.Client.ApplicationPreferences(context.Background())
	if err != nil || !reflect.DeepEqual(got, (preferences.Config{CaptureEnabled: true, MenuBarItemEnabled: true})) {
		t.Fatalf("first-run preferences = %+v, %v", got, err)
	}
	got.CaptureEnabled = false
	if err := app.Client.ConfigureApplicationPreferences(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}

	app, err = openLocalAppAt(context.Background(), directory, secrets, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	got, err = app.Client.ApplicationPreferences(context.Background())
	if err != nil || got.CaptureEnabled {
		t.Fatalf("explicit pause was not persisted: %+v, %v", got, err)
	}
}

func TestApplicationPreferencesOptInStartsBridgeAndRollsBackWhenSaveFails(t *testing.T) {
	store, err := preferences.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	control := &testMCPControl{}
	client := NewClient(&testData{}).WithPreferences(store).WithMCPControl(control)
	config, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	config.ClaudeMCPEnabled = true
	if err := client.ConfigureApplicationPreferences(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if !control.active || len(control.states) != 1 || !control.states[0] {
		t.Fatalf("MCP opt-in did not start bridge: %+v", control)
	}

	invalid := config
	invalid.ClaudeMCPEnabled = false
	invalid.ExcludedApps = []string{"not/a/valid/app"}
	if err := client.ConfigureApplicationPreferences(context.Background(), invalid); !errors.Is(err, preferences.ErrSettingsUnavailable) {
		t.Fatalf("invalid settings save error = %v", err)
	}
	if !control.active || len(control.states) != 3 || control.states[1] || !control.states[2] {
		t.Fatalf("failed preference write did not roll bridge back: %+v", control)
	}
	got, err := store.Load(context.Background())
	if err != nil || !got.ClaudeMCPEnabled {
		t.Fatalf("failed write changed persisted opt-in: config=%+v err=%v", got, err)
	}
}
