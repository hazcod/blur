package main

import (
	"github.com/hazcod/blur/internal/acceptance"
	"github.com/hazcod/blur/internal/client"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/darwin"
)

func desktopSecretStore() (platform.SecretStore, error) {
	if acceptance.Enabled {
		config, err := acceptance.Load()
		if err != nil {
			return nil, client.ErrNotConfigured
		}
		return darwin.NewScopedSecretStore(config.SecretService)
	}
	return darwin.NewSecretStore(), nil
}

func desktopDataDirectory() (string, error) {
	return client.LocalDataDirectory()
}
