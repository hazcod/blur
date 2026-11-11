package power

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type fixedPowerStateSource struct {
	state SystemState
	err   error
}

func (s fixedPowerStateSource) ReadSystemState(context.Context) (SystemState, error) {
	return s.state, s.err
}

func TestControllerDefaultsToFollowSystemWithoutWritingPreference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "power.json")
	controller, err := NewController(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := controller.Config(context.Background())
	if err != nil || got != DefaultConfig() {
		t.Fatalf("default config = %+v, %v; want %+v", got, err, DefaultConfig())
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default load created preference file: %v", err)
	}
}

func TestControllerPersistsOnlyValidatedPrivateSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "power.json")
	controller, err := NewController(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Mode: Saver, MaxBackgroundTokens: 1024}
	if err := controller.Configure(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := controller.Config(context.Background())
	if err != nil || got != want {
		t.Fatalf("persisted config = %+v, %v; want %+v", got, err, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("power settings permissions = %04o, want 0600", info.Mode().Perm())
	}
	if err := controller.Configure(context.Background(), Config{Mode: "invalid"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid config error = %v, want ErrInvalidConfig", err)
	}
}

func TestControllerRejectsUnsafeOrMalformedSettings(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "power.json")
	if err := os.WriteFile(path, []byte(`{"mode":"balanced","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	controller, err := NewController(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Config(context.Background()); !errors.Is(err, ErrSettingsUnavailable) {
		t.Fatalf("unknown JSON field error = %v, want ErrSettingsUnavailable", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"mode":"balanced"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		unsafeInfo, err := os.Stat(path)
		if err != nil || unsafeInfo.Mode().Perm()&0o077 == 0 {
			t.Fatalf("test setup did not create a group/world-readable file: info=%v err=%v", unsafeInfo, err)
		}
		if _, err := controller.Config(context.Background()); !errors.Is(err, ErrSettingsUnavailable) {
			t.Fatalf("over-permissioned file error = %v, want ErrSettingsUnavailable", err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte(`{"mode":"balanced"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := controller.Config(context.Background()); !errors.Is(err, ErrSettingsUnavailable) {
		t.Fatalf("symlink error = %v, want ErrSettingsUnavailable", err)
	}
}

func TestControllerHonorsCanceledContext(t *testing.T) {
	controller, err := NewController(filepath.Join(t.TempDir(), "power.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := controller.Config(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Config canceled error = %v", err)
	}
	if err := controller.Configure(ctx, Config{Mode: Balanced}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Configure canceled error = %v", err)
	}
}

func TestControllerEffectiveUsesBalancedFallbackForUnknownSystemState(t *testing.T) {
	controller, err := NewController(filepath.Join(t.TempDir(), "power.json"))
	if err != nil {
		t.Fatal(err)
	}
	controller.system = fixedPowerStateSource{err: ErrSystemStateUnavailable}
	effective, err := controller.Effective(context.Background())
	if !errors.Is(err, ErrSystemStateUnavailable) {
		t.Fatalf("Effective error = %v, want unknown system signal", err)
	}
	if effective.Mode != Balanced || effective.MaxBackgroundAIJobs != 1 {
		t.Fatalf("unknown system signal resolved to %+v, want Balanced with one bounded local slot", effective)
	}
}
