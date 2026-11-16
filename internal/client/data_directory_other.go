//go:build !darwin

package client

import (
	"os"
	"path/filepath"
)

func localDataDirectory() (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", storageStartupFailure("configuration_directory", ErrNotConfigured)
	}
	return filepath.Join(config, "Blur"), nil
}

func sandboxed() bool { return false }
