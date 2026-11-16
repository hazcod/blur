package client

import (
	"errors"
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

func chooseMarkdownArchive(name string) (string, error) {
	path, err := darwin.ChooseMarkdownArchive(name)
	if errors.Is(err, darwin.ErrSaveFileCancelled) {
		return "", errMarkdownArchiveCancelled
	}
	return path, err
}

func supportsMarkdownArchiveExport() bool { return true }
