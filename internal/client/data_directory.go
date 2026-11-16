package client

import "github.com/hazcod/blur/internal/acceptance"

// LocalDataDirectory is shared by desktop startup and its MCP client. Existing
// macOS container stores remain in place when the app becomes unsandboxed.
func LocalDataDirectory() (string, error) {
	if acceptance.Enabled {
		config, err := acceptance.Load()
		if err != nil {
			return "", ErrNotConfigured
		}
		return config.DataDirectory(), nil
	}
	return localDataDirectory()
}
