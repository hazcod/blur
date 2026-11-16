package client

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/notes"
)

var (
	errMarkdownArchiveClosed  = errors.New("markdown archive is closed")
	errNotesArchiveCatchingUp = errors.New("older notes are still being prepared")
	errNoSavedNotes           = errors.New("no saved notes to export")
)

const noteArchivePageSize = 12

type markdownNoteArchiveWriter struct {
	archive *zip.Writer
	names   map[string]struct{}
	closed  bool
}

func newMarkdownNoteArchiveWriter(output io.Writer) *markdownNoteArchiveWriter {
	return &markdownNoteArchiveWriter{archive: zip.NewWriter(output), names: make(map[string]struct{})}
}

func (w *markdownNoteArchiveWriter) Add(document notes.Document) error {
	if w == nil || w.archive == nil || w.closed {
		return errMarkdownArchiveClosed
	}
	if !validMarkdownArchiveID(document.ID) || !validMarkdownArchiveID(document.SourceID) || document.Revision == 0 || len(document.Text) == 0 || len(document.Text) > notes.MaxNoteBytes || !utf8.ValidString(document.Text) || strings.ContainsRune(document.Text, 0) {
		return ErrInvalidRequest
	}
	name := w.uniqueName(markdownNoteFilename(document.Text))
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(0600)
	if !document.UpdatedAt.IsZero() {
		header.Modified = document.UpdatedAt
	}
	file, err := w.archive.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.WriteString(file, document.Text)
	return err
}

func (w *markdownNoteArchiveWriter) uniqueName(name string) string {
	extension := filepath.Ext(name)
	base := strings.TrimSuffix(name, extension)
	for suffix := 1; ; suffix++ {
		candidate := name
		if suffix > 1 {
			candidate = base + " (" + strconv.Itoa(suffix) + ")" + extension
		}
		key := strings.ToLower(candidate)
		if _, exists := w.names[key]; !exists {
			w.names[key] = struct{}{}
			return candidate
		}
	}
}

func (w *markdownNoteArchiveWriter) Close() error {
	if w == nil || w.archive == nil || w.closed {
		return errMarkdownArchiveClosed
	}
	w.closed = true
	return w.archive.Close()
}

func validMarkdownArchiveID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := range value {
		if value[i] < '0' || value[i] > '9' {
			if value[i] < 'a' || value[i] > 'f' {
				return false
			}
		}
	}
	return true
}

func writeMarkdownNotesArchive(ctx context.Context, output io.Writer, documents noteDocumentPageReader) (int, error) {
	if ctx == nil || output == nil || documents == nil {
		return 0, ErrInvalidRequest
	}
	archive := newMarkdownNoteArchiveWriter(output)
	closed := false
	defer func() {
		if !closed {
			_ = archive.Close()
		}
	}()
	var beforeAt time.Time
	var beforeID string
	seenCursors, seenDocuments := map[string]bool{}, map[string]bool{}
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		page, err := documents.ListNoteDocumentsPage(ctx, notes.DocumentPageRequest{BeforeUpdatedAt: beforeAt, BeforeID: beforeID, Limit: noteArchivePageSize, MaxBytes: MaxSearchBytes})
		if err != nil {
			return 0, err
		}
		if len(page.Documents) > noteArchivePageSize || page.HasMore && (page.NextUpdatedAt.IsZero() || page.NextID == "") {
			return 0, ErrInvalidRequest
		}
		for _, document := range page.Documents {
			if seenDocuments[document.ID] {
				return 0, ErrInvalidRequest
			}
			seenDocuments[document.ID] = true
			if err := archive.Add(document); err != nil {
				return 0, err
			}
			count++
		}
		if page.CatchingUp {
			return 0, errNotesArchiveCatchingUp
		}
		if !page.HasMore {
			break
		}
		cursorKey := page.NextUpdatedAt.UTC().Format(time.RFC3339Nano) + "\x00" + page.NextID
		if seenCursors[cursorKey] || !beforeAt.IsZero() && (page.NextUpdatedAt.After(beforeAt) || page.NextUpdatedAt.Equal(beforeAt) && page.NextID >= beforeID) {
			return 0, ErrInvalidRequest
		}
		seenCursors[cursorKey] = true
		beforeAt, beforeID = page.NextUpdatedAt, page.NextID
	}
	if err := archive.Close(); err != nil {
		closed = true
		return 0, err
	}
	closed = true
	return count, nil
}

type stagedMarkdownNotesArchive struct{ temporary, destination string }

func (s *stagedMarkdownNotesArchive) discard() {
	if s != nil {
		_ = os.Remove(s.temporary)
	}
}

func (s *stagedMarkdownNotesArchive) commit() error { return os.Rename(s.temporary, s.destination) }

func stageMarkdownNotesArchive(path string, build func(io.Writer) (int, error)) (*stagedMarkdownNotesArchive, int, error) {
	if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".zip") || build == nil {
		return nil, 0, ErrInvalidRequest
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".blur-notes-*.zip")
	if err != nil {
		return nil, 0, err
	}
	stage := &stagedMarkdownNotesArchive{temporary: file.Name(), destination: path}
	failed := true
	defer func() {
		if failed {
			stage.discard()
		}
	}()
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	count, buildErr := build(file)
	if buildErr != nil {
		_ = file.Close()
		return nil, 0, buildErr
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	if err := file.Close(); err != nil {
		return nil, 0, err
	}
	failed = false
	return stage, count, nil
}
