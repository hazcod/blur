//go:build linux

package client

import (
	"errors"

	"github.com/golang-gui/goui/ui"
)

func supportsMarkdownExport() bool        { return true }
func supportsMarkdownArchiveExport() bool { return true }

func requestMarkdownExport(app ui.App, name string, done func(string, error)) {
	requestLinuxSavePath(app, ui.DialogOptions{
		Title:       "Export Markdown",
		DefaultName: name,
		Filters:     []ui.FileFilter{{Name: "Markdown", Patterns: []string{"*.md"}}},
	}, func(path string, err error) {
		if errors.Is(err, errLinuxPickerCancelled) {
			err = errMarkdownExportCancelled
		}
		done(path, err)
	})
}

func requestMarkdownArchive(app ui.App, name string, done func(string, error)) {
	requestLinuxSavePath(app, ui.DialogOptions{
		Title:       "Export notes",
		DefaultName: name,
		Filters:     []ui.FileFilter{{Name: "ZIP archive", Patterns: []string{"*.zip"}}},
	}, func(path string, err error) {
		if errors.Is(err, errLinuxPickerCancelled) {
			err = errMarkdownArchiveCancelled
		}
		done(path, err)
	})
}
