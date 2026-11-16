//go:build windows

package client

import (
	"errors"
	"path/filepath"
	"testing"

	platformwindows "github.com/hazcod/blur/internal/platform/windows"
)

func TestWindowsModelSettingsRejectBroaderACLs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-ai.json")
	if err := writeLocalAISettings(path, LocalAISettings{}); err != nil {
		t.Fatal(err)
	}
	// The log policy grants SYSTEM as well as the user. Model settings use the
	// stricter storage policy with exactly one current-user grant.
	if err := platformwindows.RestrictPathToCurrentUser(path); err != nil {
		t.Fatal(err)
	}
	if _, err := readLocalAISettings(path); !errors.Is(err, errLocalAISettings) {
		t.Fatal("model settings accepted a broader ACL", err)
	}
	indexPath := filepath.Join(t.TempDir(), "memory-index.json")
	if err := writeMemoryIndexSettings(indexPath, DefaultMemoryIndexSettings()); err != nil {
		t.Fatal(err)
	}
	if err := platformwindows.RestrictPathToCurrentUser(indexPath); err != nil {
		t.Fatal(err)
	}
	if _, err := readMemoryIndexSettings(indexPath); !errors.Is(err, errMemoryIndexSettings) {
		t.Fatal("search settings accepted a broader ACL", err)
	}
}
