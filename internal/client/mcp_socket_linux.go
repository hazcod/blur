//go:build linux

package client

import (
	"os"
	"path/filepath"
)

// userTempDir is the per-user runtime directory (XDG_RUNTIME_DIR, typically
// /run/user/<uid>, mode 0700). When it is unset the socket falls back to the
// data directory.
func userTempDir() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" || !filepath.IsAbs(dir) {
		return ""
	}
	return filepath.Clean(dir)
}
