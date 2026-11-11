package client

import "github.com/hazcod/blur/internal/platform/darwin"

const uiSnapshotSupported = true

func snapshotWindowToFile(handle uintptr, path string) error {
	return darwin.SnapshotWindow(handle, path)
}

func displayWindowNow(handle uintptr) { darwin.DisplayWindowNow(handle) }
