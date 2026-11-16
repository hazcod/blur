//go:build darwin

package client

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/platform/darwin"
)

func localDataDirectory() (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", storageStartupFailure("configuration_directory", ErrNotConfigured)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", storageStartupFailure("configuration_directory", ErrNotConfigured)
	}
	if realHome := legacyHomeFromContainer(home); realHome != "" {
		home = realHome
	}
	if !sandboxed() {
		// A launcher may leave HOME remapped into another app's container.
		// Only a signed sandbox compatibility build uses the remapped config.
		config = filepath.Join(home, "Library", "Application Support")
	}
	container := filepath.Join(home, "Library", "Containers", appidentity.BundleID, "Data", "Library", "Application Support", "Blur")
	return resolveMacOSDataDirectory(filepath.Join(config, "Blur"), container, os.Lstat)
}

func sandboxed() bool { return darwin.AppSandboxEnabled() }

func resolveMacOSDataDirectory(normal, container string, lstat func(string) (os.FileInfo, error)) (string, error) {
	// Already inside the app's container, including its first sandboxed launch.
	if filepath.Clean(normal) == filepath.Clean(container) {
		return normal, nil
	}
	info, err := lstat(container)
	if errors.Is(err, os.ErrNotExist) {
		return normal, nil
	}
	if err != nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		// Never hide an inaccessible existing store by creating another one.
		return "", storageStartupFailure("storage_directory", ErrNotConfigured)
	}
	return container, nil
}
