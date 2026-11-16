package client

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/notes"
)

func TestMarkdownExportKeepsExactTextAndAtomicallyReplacesDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "My note.md")
	if err := os.WriteFile(path, []byte("older export"), 0600); err != nil {
		t.Fatal(err)
	}
	text := "# Review\n\n- [ ] Check rollout\n- Mira owns rollback.\n\n```sh\necho hello\n```\n"
	if err := writeMarkdownExport(path, text); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != text {
		t.Fatal("Markdown was altered or not exported", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("export permissions are not private", err)
	}
	if err := writeMarkdownExport(path, "bad\x00input"); err == nil {
		t.Fatal("invalid text replaced the existing export")
	}
	got, _ = os.ReadFile(path)
	if string(got) != text {
		t.Fatal("failed export changed the destination")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatal("temporary export files were left behind")
	}
}

func TestMarkdownFilenameUsesReadableSafeTitle(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"# Rollout notes\n\nMira", "Rollout notes.md"},
		{"## Café / rollout: review", "Café  rollout review.md"},
		{"\n\n", "Blur note.md"},
		{".md", "md.md"},
	} {
		if got := markdownNoteFilename(tc.text); got != tc.want {
			t.Fatalf("filename=%q want=%q", got, tc.want)
		}
	}
	got := markdownNoteFilename(strings.Repeat("é", 500))
	if len([]rune(got)) > 83 || filepath.Base(got) != got {
		t.Fatal("unbounded or unsafe file name")
	}
}

func TestMarkdownArchiveKeepsExactNotesAndDisambiguatesDuplicateTitles(t *testing.T) {
	var output bytes.Buffer
	archive := newMarkdownNoteArchiveWriter(&output)
	documents := []notes.Document{
		{ID: strings.Repeat("1", 64), SourceID: strings.Repeat("a", 64), Revision: 1, Text: "# Roadmap\n\n- Keep the source link.\n"},
		{ID: strings.Repeat("2", 64), SourceID: strings.Repeat("b", 64), Revision: 1, Text: "# Roadmap\n\n- Review the rollout.\n"},
		{ID: strings.Repeat("3", 64), SourceID: strings.Repeat("c", 64), Revision: 1, Text: "# roadmap\n\nKeep case-distinct title safe.\n"},
	}
	for _, document := range documents {
		if err := archive.Add(document); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal("bulk export is not a readable ZIP archive", err)
	}
	wantNames := []string{"Roadmap.md", "Roadmap (2).md", "roadmap (3).md"}
	if len(reader.File) != len(documents) {
		t.Fatalf("archive has %d files, want %d", len(reader.File), len(documents))
	}
	for index, file := range reader.File {
		if file.Name != wantNames[index] || file.Mode().Perm()&0077 != 0 {
			t.Errorf("archive entry %d has unsafe or ambiguous metadata: name=%q mode=%v", index, file.Name, file.Mode().Perm())
		}
		input, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		_, copyErr := got.ReadFrom(input)
		closeErr := input.Close()
		if copyErr != nil || closeErr != nil || got.String() != documents[index].Text {
			t.Errorf("archive changed note %q: %q err=%v close=%v", file.Name, got.String(), copyErr, closeErr)
		}
	}
}

func TestMarkdownNotesArchivePagesEncryptedDocumentsWithoutOmittingContent(t *testing.T) {
	c, store, _ := todayTaskClient(t)
	want := make(map[string]bool)
	for i := range 13 {
		title := "Note " + string(rune('A'+i))
		if i < 2 {
			title = "Shared title"
		}
		text := "# " + title + "\n\nBody " + string(rune('A'+i)) + " with the original Markdown.\n"
		if _, err := c.CreateNoteDocument(context.Background(), text); err != nil {
			t.Fatal(err)
		}
		want[text] = true
	}
	var output bytes.Buffer
	count, err := writeMarkdownNotesArchive(context.Background(), &output, store)
	if err != nil || count != len(want) {
		t.Fatalf("archive export count=%d want=%d err=%v", count, len(want), err)
	}
	archive, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil || len(archive.File) != len(want) {
		t.Fatalf("archive contains %d notes, want %d: %v", len(archive.File), len(want), err)
	}
	seen := make(map[string]bool, len(want))
	for _, file := range archive.File {
		input, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		var text bytes.Buffer
		_, readErr := text.ReadFrom(input)
		closeErr := input.Close()
		if readErr != nil || closeErr != nil || !want[text.String()] || seen[text.String()] {
			t.Fatalf("archive omitted, changed, or duplicated a note in %q: %q read=%v close=%v", file.Name, text.String(), readErr, closeErr)
		}
		seen[text.String()] = true
	}
	for text := range want {
		if !seen[text] {
			t.Fatalf("archive omitted encrypted note content %q", text)
		}
	}
}

func TestSavedMemoryExportsAllNotesFromItsMarkdownAction(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	const text = "# Exported from Ask\n\nExact Markdown note.\n"
	if _, err := c.CreateNoteDocument(context.Background(), text); err != nil {
		t.Fatal(err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 8)}
	destination := filepath.Join(t.TempDir(), "Blur Notes.zip")
	if err := os.WriteFile(destination, []byte("previous archive"), 0600); err != nil {
		t.Fatal(err)
	}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	d.memoryUI.loaded = true
	d.memoryUI.chooseNotesExport = func(name string) (string, error) {
		if name != "Blur Notes.zip" {
			t.Errorf("archive picker suggested %q", name)
		}
		return destination, nil
	}
	_, buttons, _ := memoryWidgetTree(t, d)
	clickMemoryButton(t, buttons, "Export notes")
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for d.memoryUI.notesExporting {
		select {
		case publish := <-app.posted:
			publish()
		case <-deadline.C:
			t.Fatal("Markdown archive export stalled", d.memoryUI.status)
		}
	}
	if !strings.Contains(d.memoryUI.status, "Exported 1 note") {
		t.Fatalf("export completion was not reported naturally: %q", d.memoryUI.status)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal("archive was not committed", err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("archive permissions are not private: info=%v err=%v", info, err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) != 1 {
		t.Fatalf("exported archive unreadable or incomplete: files=%d err=%v", len(archive.File), err)
	}
	file, err := archive.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	_, readErr := got.ReadFrom(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || got.String() != text {
		t.Fatalf("export changed the saved Markdown: %q read=%v close=%v", got.String(), readErr, closeErr)
	}
}
