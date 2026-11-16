package client

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/notes"
)

var errMarkdownExportCancelled = errors.New("markdown export canceled")

func markdownNoteHeading(text string) (title, body string, found bool) {
	lines := strings.SplitAfter(text, "\n")
	for index, line := range lines {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\n"))
		if line == "" {
			continue
		}
		hashes := len(line) - len(strings.TrimLeft(line, "#"))
		if hashes < 1 || hashes > 6 || len(line) <= hashes || line[hashes] != ' ' {
			return "", text, false
		}
		title = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(line[hashes+1:]), "#"))
		if title == "" {
			return "", text, false
		}
		return title, strings.TrimLeft(strings.Join(lines[index+1:], ""), "\r\n"), true
	}
	return "", text, false
}

func markdownNoteFilename(text string) string {
	title, _, found := markdownNoteHeading(text)
	if !found {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				title = line
				break
			}
		}
	}
	var name []rune
	for _, r := range title {
		if unicode.IsControl(r) || strings.ContainsRune("/\\:", r) {
			continue
		}
		name = append(name, r)
		if len(name) == 80 {
			break
		}
	}
	base := strings.Trim(string(name), " .")
	if base == "" {
		base = "Blur note"
	}
	if !strings.EqualFold(filepath.Ext(base), ".md") {
		base += ".md"
	}
	return base
}

// Staging performs directory I/O, writing and fsync before entering a memory
// publication fence. Only the final atomic rename belongs inside that fence.
type stagedMarkdownExport struct{ temporary, destination string }

func (s *stagedMarkdownExport) discard() {
	if s != nil {
		_ = os.Remove(s.temporary)
	}
}
func (s *stagedMarkdownExport) commit() error { return os.Rename(s.temporary, s.destination) }

func stageMarkdownExport(path, text string) (*stagedMarkdownExport, error) {
	if !filepath.IsAbs(path) || filepath.Ext(path) == "" || len(text) == 0 || len(text) > notes.MaxNoteBytes || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return nil, ErrInvalidRequest
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".blur-markdown-*")
	if err != nil {
		return nil, err
	}
	stage := &stagedMarkdownExport{temporary: f.Name(), destination: path}
	failed := true
	defer func() {
		if failed {
			stage.discard()
		}
	}()
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	failed = false
	return stage, nil
}

// Direct helper for exports without a saved-memory publication requirement.
func writeMarkdownExport(path, text string) error {
	stage, err := stageMarkdownExport(path, text)
	if err != nil {
		return err
	}
	defer stage.discard()
	return stage.commit()
}
