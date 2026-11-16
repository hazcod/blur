//go:build darwin && !blur_acceptance

package client

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsandboxedStartupReusesContainerMemoryAndSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(sandboxContainerEnv, "")
	container := filepath.Join(home, "Library", "Containers", "com.hazcod.blur", "Data", "Library", "Application Support", "Blur")
	if err := os.MkdirAll(filepath.Dir(container), 0o700); err != nil {
		t.Fatal(err)
	}
	secrets := &bootstrapSecrets{values: make(map[string][]byte)}
	ctx := context.Background()
	previous, err := openLocalAppAt(ctx, container, secrets, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := previous.Client.AddNote("Mira owns the rollback."); err != nil {
		_ = previous.Close()
		t.Fatal(err)
	}
	settings, err := previous.Client.ApplicationPreferences(ctx)
	if err != nil {
		_ = previous.Close()
		t.Fatal(err)
	}
	settings.CaptureEnabled = false
	settings.QuickAskHotkey = "ctrl+space"
	if err := previous.Client.ConfigureApplicationPreferences(ctx, settings); err != nil {
		_ = previous.Close()
		t.Fatal(err)
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}

	current, err := OpenLocalApp(ctx, secrets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })
	hits, err := current.Client.Search("Mira")
	if err != nil || len(hits.Hits) != 1 {
		t.Fatalf("existing memory was abandoned: hits=%+v err=%v", hits, err)
	}
	got, err := current.Client.ApplicationPreferences(ctx)
	if err != nil || got.CaptureEnabled || got.QuickAskHotkey != "ctrl+space" {
		t.Fatalf("existing settings were abandoned: settings=%+v err=%v", got, err)
	}
	legacy := filepath.Join(home, "Library", "Application Support", "Blur", "memory.db")
	if _, err := os.Lstat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup created a second database: %v", err)
	}
}

func TestMacOSDataDirectorySelectionPreservesExistingContainerAndFailsClosed(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "file", "symlink", "denied", "already sandboxed"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			normal, container := filepath.Join(root, "legacy"), filepath.Join(root, "container")
			stat := os.Lstat
			want, fail := normal, false
			switch kind {
			case "directory":
				if err := os.Mkdir(container, 0o700); err != nil {
					t.Fatal(err)
				}
				// Even a later legacy store must not replace the container chosen by the current app.
				if err := os.Mkdir(normal, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(normal, "memory.db"), []byte("newer legacy fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = container
			case "file":
				if err := os.WriteFile(container, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				fail = true
			case "symlink":
				if err := os.Mkdir(normal, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(normal, container); err != nil {
					t.Fatal(err)
				}
				fail = true
			case "denied":
				stat = func(string) (os.FileInfo, error) { return nil, os.ErrPermission }
				fail = true
			case "already sandboxed":
				normal, want = container, container
				stat = func(string) (os.FileInfo, error) {
					t.Fatal("sandbox startup probed outside its chosen config directory")
					return nil, os.ErrPermission
				}
			}
			got, err := resolveMacOSDataDirectory(normal, container, stat)
			if fail {
				if got != "" || !errors.Is(err, ErrNotConfigured) || storageStartupStage(err) != "storage_directory" {
					t.Fatalf("unsafe fallback: path=%q err=%v", got, err)
				}
			} else if err != nil || got != want {
				t.Fatalf("path=%q want=%q err=%v", got, want, err)
			}
		})
	}
}

func TestUnsandboxedProcessIgnoresInheritedSandboxEnvironment(t *testing.T) {
	t.Setenv(sandboxContainerEnv, "com.hazcod.blur")
	t.Setenv("HOME", filepath.Join(t.TempDir(), "Library", "Containers", "another.app", "Data"))
	if sandboxed() {
		t.Fatal("inherited environment can authorize a legacy-store overwrite")
	}
}

func TestUnsandboxedRemappedHomeUsesRealHomeLegacyDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", filepath.Join(home, "Library", "Containers", "another.app", "Data"))
	t.Setenv(sandboxContainerEnv, "")
	got, err := LocalDataDirectory()
	want := filepath.Join(home, "Library", "Application Support", "Blur")
	if err != nil || got != want {
		t.Fatalf("remapped HOME selected a different store: path=%q want=%q err=%v", got, want, err)
	}
}
