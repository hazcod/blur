package client

import (
	"errors"

	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/platform/darwin"
)

func chooseMarkdownExport(name string) (string, error) {
	path, err := darwin.ChooseMarkdownExport(name)
	if errors.Is(err, darwin.ErrSaveFileCancelled) {
		return "", errMarkdownExportCancelled
	}
	return path, err
}
func supportsMarkdownExport() bool { return true }

// requestMarkdownExport runs the modal AppKit save panel and reports inline.
func requestMarkdownExport(_ ui.App, name string, done func(string, error)) {
	done(chooseMarkdownExport(name))
}

func chooseMarkdownArchive(name string) (string, error) {
	path, err := darwin.ChooseMarkdownArchive(name)
	if errors.Is(err, darwin.ErrSaveFileCancelled) {
		return "", errMarkdownArchiveCancelled
	}
	return path, err
}

func supportsMarkdownArchiveExport() bool { return true }

// requestMarkdownArchive runs the modal AppKit save panel and reports inline.
func requestMarkdownArchive(_ ui.App, name string, done func(string, error)) {
	done(chooseMarkdownArchive(name))
}
