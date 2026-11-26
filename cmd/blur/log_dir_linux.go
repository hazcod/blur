//go:build linux && (amd64 || arm64)

package main

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// defaultLogsDirectory follows the XDG base directory specification: logs are
// state, kept in $XDG_STATE_HOME (default ~/.local/state).
func defaultLogsDirectory() (string, error) {
	if state := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(state) {
		return filepath.Join(state, "Blur", "Logs"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("user home directory unavailable")
	}
	return filepath.Join(home, ".local", "state", "Blur", "Logs"), nil
}

// stderrIsTerminal reports a terminal launch, whose diagnostics stay on the
// terminal instead of moving to the log file.
func stderrIsTerminal() bool {
	_, err := unix.IoctlGetTermios(2, unix.TCGETS)
	return err == nil
}
