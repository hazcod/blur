package darwin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultFolderStartPrefersExistingThenICloudThenHome(t *testing.T) {
	home := t.TempDir()
	if got := DefaultFolderStart(home, ""); got != home {
		t.Fatalf("no iCloud: %q", got)
	}
	icloud := filepath.Join(home, iCloudDriveRelative)
	if err := os.MkdirAll(icloud, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := DefaultFolderStart(home, ""); got != icloud {
		t.Fatalf("iCloud: %q", got)
	}
	if got := DefaultFolderStart(home, filepath.Join(home, "missing")); got != icloud {
		t.Fatalf("missing preferred: %q", got)
	}
	preferred := filepath.Join(home, "Sync")
	if err := os.MkdirAll(preferred, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := DefaultFolderStart(home, preferred); got != preferred {
		t.Fatalf("preferred: %q", got)
	}
	if got := DefaultFolderStart("", ""); got != "" {
		t.Fatalf("empty: %q", got)
	}
}

func TestResolveBookmarkRejectsBadInput(t *testing.T) {
	if _, _, _, err := ResolveBookmark(nil); err == nil {
		t.Fatal("empty bookmark accepted")
	}
	if _, _, _, err := ResolveBookmark(make([]byte, MaxBookmarkBytes+1)); err == nil {
		t.Fatal("oversized bookmark accepted")
	}
}
