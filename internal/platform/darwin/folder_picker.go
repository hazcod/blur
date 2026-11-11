package darwin

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrFolderPickerUnavailable is returned when the folder picker cannot run
// (non-macOS builds, or the AppKit classes are missing).
var ErrFolderPickerUnavailable = errors.New("darwin: folder picker unavailable")

// ErrFolderPickerCancelled is returned when the user dismisses the panel.
var ErrFolderPickerCancelled = errors.New("darwin: folder picker cancelled")

// ErrBookmarkInvalid is returned when saved bookmark data cannot be resolved.
var ErrBookmarkInvalid = errors.New("darwin: folder bookmark invalid")

// MaxBookmarkBytes bounds persisted security-scoped bookmark data.
const MaxBookmarkBytes = 64 << 10

// iCloudDriveRelative is where iCloud Drive is mounted below the home folder.
const iCloudDriveRelative = "Library/Mobile Documents/com~apple~CloudDocs"

// DefaultFolderStart returns the directory the folder picker should open on:
// the preferred path when it exists, else iCloud Drive, else the home folder.
func DefaultFolderStart(home, preferred string) string {
	isDir := func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.IsDir()
	}
	if preferred != "" && isDir(preferred) {
		return preferred
	}
	if home == "" {
		return ""
	}
	if icloud := filepath.Join(home, iCloudDriveRelative); isDir(icloud) {
		return icloud
	}
	return home
}
