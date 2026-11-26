//go:build linux

package client

import (
	"errors"

	"github.com/golang-gui/goui/ui"
)

// errLinuxPickerCancelled reports a closed picker, or a second request while
// one is already open for the same window.
var errLinuxPickerCancelled = errors.New("client: file picker canceled")

const linuxPickerOwner = "main"

func requestLinuxSavePath(app ui.App, opts ui.DialogOptions, done func(string, error)) {
	requestLinuxPath(app, done, func(dialog ui.FileDialog, owner string, cb func([]ui.FileAccess, error)) error {
		return dialog.SaveFile(owner, opts, cb)
	})
}

func requestLinuxDirectory(app ui.App, opts ui.DialogOptions, done func(string, error)) {
	requestLinuxPath(app, done, func(dialog ui.FileDialog, owner string, cb func([]ui.FileAccess, error)) error {
		return dialog.OpenDirectory(owner, opts, cb)
	})
}

// requestLinuxPath opens a desktop-portal (or zenity/kdialog) picker and
// reports one local path on the UI thread. Access is released before done
// runs: unsandboxed Linux builds use the returned path directly.
func requestLinuxPath(app ui.App, done func(string, error), open func(ui.FileDialog, string, func([]ui.FileAccess, error)) error) {
	if app == nil {
		done("", errDesktopAppUnavailable)
		return
	}
	owner := ""
	if app.FindWindow(linuxPickerOwner) != nil {
		owner = linuxPickerOwner
	}
	err := open(app.FileDialog(), owner, func(files []ui.FileAccess, err error) {
		done(linuxPickerResult(files, err))
	})
	if err != nil {
		done("", err)
	}
}

func linuxPickerResult(files []ui.FileAccess, err error) (string, error) {
	defer func() {
		for _, file := range files {
			_ = file.Close()
		}
	}()
	switch {
	case errors.Is(err, ui.ErrDialogBusy):
		return "", errLinuxPickerCancelled
	case err != nil:
		return "", err
	case len(files) == 0:
		return "", errLinuxPickerCancelled
	}
	return files[0].LocalPath()
}
