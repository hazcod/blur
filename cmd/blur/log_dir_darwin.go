package main

import (
	"errors"
	"os"
	"path/filepath"
)

func defaultLogsDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("user home directory unavailable")
	}
	return filepath.Join(home, "Library", "Logs", "Blur"), nil
}
