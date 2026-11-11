//go:build windows

package windows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hazcod/blur/internal/platform"
)

func TestSecretStoreRoundTripAndDelete(t *testing.T) {
	store := &SecretStore{dir: t.TempDir()}
	ctx := context.Background()
	if _, err := store.Get(ctx, "master-key", 32); !hasSecretState(err, platform.SecretNotFound) {
		t.Fatalf("Get missing state = %v, want not_found", err)
	}
	want := []byte("0123456789abcdef0123456789abcdef")
	if err := store.Put(ctx, "master-key", want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get(ctx, "master-key", len(want))
	if err != nil || string(got) != string(want) {
		t.Fatalf("Get = %x, %v; want round-trip", got, err)
	}
	got[0] ^= 0xff
	got, err = store.Get(ctx, "master-key", len(want))
	if err != nil || string(got) != string(want) {
		t.Fatalf("Get result was not isolated: %x, %v", got, err)
	}
	if err := store.Delete(ctx, "master-key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx, "master-key", 32); !hasSecretState(err, platform.SecretNotFound) {
		t.Fatalf("Get after Delete state = %v, want not_found", err)
	}
}

func TestSecretStoreRejectsCorruptAndOversizedBlobs(t *testing.T) {
	store := &SecretStore{dir: t.TempDir()}
	path := store.path("master-key")
	if err := os.WriteFile(path, []byte("not a DPAPI blob"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "master-key", 32); !hasSecretState(err, platform.SecretCorrupt) {
		t.Fatalf("Get corrupt state = %v, want corrupt", err)
	}
	if err := os.WriteFile(path, make([]byte, maxSecretFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "master-key", 32); !hasSecretState(err, platform.SecretCorrupt) {
		t.Fatalf("Get oversized state = %v, want corrupt", err)
	}
}

func TestSecretStoreChecksContextAndLength(t *testing.T) {
	store := &SecretStore{dir: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Get(ctx, "key", 32); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get canceled = %v", err)
	}
	if err := store.Put(ctx, "key", make([]byte, 32)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put canceled = %v", err)
	}
	if err := store.Put(context.Background(), "key", []byte("short")); err != nil {
		t.Fatalf("Put test value: %v", err)
	}
	got, err := store.Get(context.Background(), "key", 32)
	if err != nil || string(got) != "short" {
		t.Fatalf("Get maximum-size read = %q, %v; want short value", got, err)
	}
	if _, err := os.Stat(filepath.Dir(store.path("key"))); err != nil {
		t.Fatalf("store directory missing: %v", err)
	}
}

func hasSecretState(err error, state platform.SecretState) bool {
	var secretErr *platform.SecretStoreError
	return errors.As(err, &secretErr) && secretErr.State == state
}
