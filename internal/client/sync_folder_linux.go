//go:build linux

package client

import (
	"errors"

	"github.com/golang-gui/goui/ui"
)

// requestSyncFolder opens the desktop folder chooser. Linux builds are not
// sandboxed, so the plain path reopens the folder and no bookmark is stored.
func requestSyncFolder(app ui.App, start string, done func(string, []byte, error)) {
	requestLinuxDirectory(app, ui.DialogOptions{Title: "Choose sync folder", InitialDir: start}, func(path string, err error) {
		if errors.Is(err, errLinuxPickerCancelled) {
			err = errSyncFolderCancelled
		}
		done(path, nil, err)
	})
}
