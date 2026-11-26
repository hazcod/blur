//go:build !darwin && !linux

package client

import "github.com/golang-gui/goui/ui"

func supportsMarkdownExport() bool        { return false }
func supportsMarkdownArchiveExport() bool { return false }
func requestMarkdownExport(_ ui.App, _ string, done func(string, error)) {
	done("", ErrNotConfigured)
}
func requestMarkdownArchive(_ ui.App, _ string, done func(string, error)) {
	done("", ErrNotConfigured)
}
