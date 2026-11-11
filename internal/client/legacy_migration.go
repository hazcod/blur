package client

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// legacyMigrationMarker is written into a migrated store directory; it only
// records that the container holds a copy of the legacy store.
const legacyMigrationMarker = "migrated-from-legacy"

const sandboxContainerEnv = "APP_SANDBOX_CONTAINER_ID"

// storeFileName is the encrypted store inside a data directory. Its presence
// is what "the store exists" means; it is also the last file moved into place.
const storeFileName = "memory.db"

type legacyMigrationInput struct {
	Sandboxed    bool
	ContainerDir string
	LegacyDir    string
}

// shouldMigrateLegacy is the pure migration decision. modTime reports a file's
// modification time and whether it exists; it is injected so tests can use a
// fake filesystem. Latest wins: migration runs only inside the sandbox, only
// when a distinct legacy store exists, and only when the container has no store
// or the legacy memory.db is strictly newer than the container one (an
// unsandboxed run kept writing the legacy store).
func shouldMigrateLegacy(in legacyMigrationInput, modTime func(string) (time.Time, bool)) bool {
	if !in.Sandboxed || in.ContainerDir == "" || in.LegacyDir == "" {
		return false
	}
	if filepath.Clean(in.ContainerDir) == filepath.Clean(in.LegacyDir) {
		return false
	}
	legacy, ok := modTime(filepath.Join(in.LegacyDir, storeFileName))
	if !ok {
		return false
	}
	container, ok := modTime(filepath.Join(in.ContainerDir, storeFileName))
	return !ok || legacy.After(container)
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func fileModTime(path string) (time.Time, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// sandboxed reports whether the process runs in an App Sandbox container. The
// sandbox sets APP_SANDBOX_CONTAINER_ID only for some launch paths (not for
// LaunchServices/open), so a HOME inside ~/Library/Containers also counts.
func sandboxed() bool {
	if os.Getenv(sandboxContainerEnv) != "" {
		return true
	}
	home, err := os.UserHomeDir()
	return err == nil && legacyHomeFromContainer(home) != ""
}

// legacyHomeFromContainer derives the real home from a sandbox container home
// (<home>/Library/Containers/<id>/Data). It is the last-resort fallback.
func legacyHomeFromContainer(containerHome string) string {
	const marker = "/Library/Containers/"
	if i := strings.Index(containerHome, marker); i > 0 {
		return containerHome[:i]
	}
	return ""
}

// migrateLegacyStore copies a pre-sandbox store directory into the container
// data directory when shouldMigrateLegacy allows it (latest wins, so it can run
// on every sandboxed launch). Legacy data is only read, never modified or
// deleted. Files are staged in a sibling temp directory, then renamed over the
// container files (atomic per file, preferences included); memory.db is renamed
// last so a partial migration keeps the older complete store. Failures leave
// the container as it was and are logged content-free.
func migrateLegacyStore(containerDir, legacyDir string) {
	in := legacyMigrationInput{Sandboxed: sandboxed(), ContainerDir: containerDir, LegacyDir: legacyDir}
	if !shouldMigrateLegacy(in, fileModTime) {
		return
	}
	n, err := copyLegacyStore(legacyDir, containerDir, rand.Reader)
	if err != nil {
		slog.Warn("legacy store migration failed", "result", "failed")
		return
	}
	slog.Info("legacy store migrated into sandbox container", "result", "ok", "files", n)
}

func copyLegacyStore(legacyDir, containerDir string, random io.Reader) (int, error) {
	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		return 0, err
	}
	parent := filepath.Dir(containerDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return 0, err
	}
	var suffix [6]byte
	if _, err := io.ReadFull(random, suffix[:]); err != nil {
		return 0, err
	}
	staging := filepath.Join(parent, "."+filepath.Base(containerDir)+".migrating-"+hex.EncodeToString(suffix[:]))
	if err := os.Mkdir(staging, 0o700); err != nil {
		return 0, err
	}
	defer os.RemoveAll(staging)
	var names []string
	hasStore := false
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || name == ".bootstrap.lock" || name == legacyMigrationMarker || strings.HasSuffix(name, ".sock") {
			continue
		}
		if err := copyRegularFile(filepath.Join(legacyDir, name), filepath.Join(staging, name)); err != nil {
			return 0, err
		}
		if name == storeFileName {
			hasStore = true
			continue
		}
		names = append(names, name)
	}
	if !hasStore {
		return 0, errors.New("legacy store vanished during copy")
	}
	if err := os.WriteFile(filepath.Join(staging, legacyMigrationMarker), nil, 0o600); err != nil {
		return 0, err
	}
	names = append(names, legacyMigrationMarker, storeFileName)
	if err := os.MkdirAll(containerDir, 0o700); err != nil {
		return 0, err
	}
	// Rename over the container files (atomic replace); the store is committed
	// last. Container-only files (lock, socket) are left alone.
	moved := 0
	for _, name := range names {
		if err := os.Rename(filepath.Join(staging, name), filepath.Join(containerDir, name)); err != nil {
			return moved, err
		}
		moved++
	}
	return moved - 1, nil
}

func copyRegularFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.Join(err, fs.ErrInvalid)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
