package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/browsercapture"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/preferences"
)

func TestSafariCapturePreferencePolicyMatchesNativeAppDomainRules(t *testing.T) {
	base := preferences.Config{CaptureEnabled: true, SafariPageCaptureEnabled: true}
	for _, test := range []struct {
		name    string
		change  func(*preferences.Config)
		domain  string
		allowed bool
	}{
		{"opted in", func(*preferences.Config) {}, "example.test", true},
		{"global off", func(c *preferences.Config) { c.CaptureEnabled = false }, "example.test", false},
		{"Safari off", func(c *preferences.Config) { c.SafariPageCaptureEnabled = false }, "example.test", false},
		{"excluded app", func(c *preferences.Config) { c.ExcludedApps = []string{"com.apple.safari"} }, "example.test", false},
		{"excluded domain", func(c *preferences.Config) { c.ExcludedDomains = []string{"example.test"} }, "nested.example.test", false},
		{"default disabled", func(c *preferences.Config) { c.CaptureDefaultDisabled = true }, "example.test", false},
		{"included domain", func(c *preferences.Config) {
			c.CaptureDefaultDisabled = true
			c.IncludedDomains = []string{"example.test"}
		}, "nested.example.test", true},
		{"other included domain", func(c *preferences.Config) {
			c.CaptureDefaultDisabled = true
			c.IncludedDomains = []string{"other.test"}
		}, "example.test", false},
		{"included app", func(c *preferences.Config) {
			c.CaptureDefaultDisabled = true
			c.IncludedApps = []string{"com.apple.safari"}
		}, "example.test", true},
		{"deny wins over app allow", func(c *preferences.Config) {
			c.CaptureDefaultDisabled = true
			c.IncludedApps = []string{"com.apple.safari"}
			c.ExcludedDomains = []string{"example.test"}
		}, "example.test", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := preferences.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
			if err != nil {
				t.Fatal(err)
			}
			config := base
			test.change(&config)
			if err := store.Save(context.Background(), config); err != nil {
				t.Fatal(err)
			}
			observation, err := capture.SanitizeObservation("com.apple.safari", test.domain, "synthetic visible page", 512)
			if err != nil {
				t.Fatal(err)
			}
			var writes int
			err = safariCapturePreferencePolicy(store)(context.Background(), observation, func(context.Context) error { writes++; return nil })
			if test.allowed && (err != nil || writes != 1) || !test.allowed && (!errors.Is(err, browsercapture.ErrRejected) || writes != 0) {
				t.Fatalf("policy allowed=%t writes=%d err=%v", test.allowed, writes, err)
			}
		})
	}
}

func TestSafariCapturePreferenceChangeSerializesWithFinalCommit(t *testing.T) {
	store, _ := preferences.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	config := preferences.Config{CaptureEnabled: true, SafariPageCaptureEnabled: true}
	if err := store.Save(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	observation, _ := capture.SanitizeObservation("com.apple.safari", "example.test", "synthetic page", 512)
	policy := safariCapturePreferencePolicy(store)
	entered, release := make(chan struct{}), make(chan struct{})
	var committed atomic.Bool
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- policy(context.Background(), observation, func(ctx context.Context) error {
			close(entered)
			select {
			case <-release:
				committed.Store(true)
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	<-entered
	config.ExcludedDomains = []string{"example.test"}
	saveStarted, saveDone := make(chan struct{}), make(chan error, 1)
	go func() { close(saveStarted); saveDone <- store.Save(context.Background(), config) }()
	<-saveStarted
	select {
	case err := <-saveDone:
		t.Fatalf("preference Save returned before prior admitted write finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-saveDone; err != nil || !committed.Load() {
		t.Fatalf("serialized commit or preference save failed: %v", err)
	}
	var laterWrites int
	if err := policy(context.Background(), observation, func(context.Context) error { laterWrites++; return nil }); !errors.Is(err, browsercapture.ErrRejected) || laterWrites != 0 {
		t.Fatal("old Safari pipeline wrote after new exclusion was saved")
	}
}

func TestSafariCapturePolicyRejectsCanceledOrUnreadablePreferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, _ := preferences.NewStore(path)
	if err := os.WriteFile(path, []byte("invalid synthetic settings"), 0o600); err != nil {
		t.Fatal(err)
	}
	observation, _ := capture.SanitizeObservation("com.apple.safari", "example.test", "synthetic page", 512)
	var writes int
	write := func(context.Context) error { writes++; return nil }
	if err := safariCapturePreferencePolicy(store)(context.Background(), observation, write); err == nil || writes != 0 {
		t.Fatal("unreadable preferences allowed Safari write")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := safariCapturePreferencePolicy(store)(ctx, observation, write); !errors.Is(err, context.Canceled) || writes != 0 {
		t.Fatal("canceled Safari write persisted")
	}
}
