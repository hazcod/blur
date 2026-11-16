//go:build !darwin

package client

func chooseMarkdownExport(string) (string, error)  { return "", ErrNotConfigured }
func supportsMarkdownExport() bool                 { return false }
func chooseMarkdownArchive(string) (string, error) { return "", ErrNotConfigured }
func supportsMarkdownArchiveExport() bool          { return false }
