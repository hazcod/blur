package client

import (
	"errors"

	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/platform/darwin"
)

// requestSyncFolder runs the modal AppKit folder panel and reports inline,
// with a security-scoped bookmark for sandboxed builds.
func requestSyncFolder(_ ui.App, start string, done func(string, []byte, error)) {
	path, bookmark, err := darwin.ChooseFolder(start)
	if errors.Is(err, darwin.ErrFolderPickerCancelled) {
		err = errSyncFolderCancelled
	}
	done(path, bookmark, err)
}
