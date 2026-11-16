package client

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"

	"github.com/hazcod/blur/internal/acceptance"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/preferences"
)

func openAcceptanceApp(ctx context.Context, secrets platform.SecretStore) (*LocalApp, error) {
	config, err := acceptance.Load()
	if err != nil {
		return nil, ErrNotConfigured
	}
	scoped, ok := secrets.(interface{ ServiceIdentity() string })
	if !ok || scoped.ServiceIdentity() != config.SecretService {
		return nil, ErrNotConfigured
	}
	if err := prepareAcceptancePreferences(ctx, config); err != nil {
		return nil, ErrNotConfigured
	}
	return openLocalAppAt(ctx, config.DataDirectory(), secrets, rand.Reader)
}

func prepareAcceptancePreferences(ctx context.Context, config acceptance.Config) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrNotConfigured
	}
	directory := config.DataDirectory()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	path := filepath.Join(directory, "preferences.json")
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	store, err := preferences.NewStore(path)
	if err != nil {
		return err
	}
	// Start with capture paused so launching the fixture cannot summon a system
	// permission flow. Its independent required scope still applies after a
	// user starts capture or edits these ordinary preferences.
	return store.Save(ctx, preferences.Config{CaptureEnabled: false, MenuBarItemEnabled: false, IncludedApps: []string{config.AppID}, IncludedDomains: []string{config.Domain}, CaptureDefaultDisabled: true})
}
