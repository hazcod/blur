package client

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-gui/goui/platform/events"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// Exercise actual UI submission handlers and encrypted persistence, replacing
// only the GUI dispatcher. No model, capture, app launch or native window runs.
func TestQuickPanelMeetingNotesPreserveBytesAcrossSaveGestures(t *testing.T) {
	for _, source := range []struct{ name, input, want string }{
		{"surrounding whitespace", "\n\t" + meetingPasteFixture + "\n  ", "\n\t" + meetingPasteFixture + "\n  "},
		{"CRLF", strings.ReplaceAll(meetingPasteFixture, "\n", "\r\n"), strings.ReplaceAll(meetingPasteFixture, "\n", "\r\n")},
		{"explicit request", "Please save these meeting notes:\n" + meetingPasteFixture + "  \n", meetingPasteFixture + "  \n"},
	} {
		for _, gesture := range []string{"Enter", "Command-S", "Command-Return"} {
			t.Run(source.name+"/"+gesture, func(t *testing.T) {
				store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = store.Close() }()
				c := NewClient(store).WithNotes(notes.NewService(store, nil, nil))
				defer c.Close()
				app := &meetingPasteApp{&pausedAskApp{posted: make(chan func(), 64)}}
				d := &desktop{app: app, client: c, quickInput: source.input}
				switch gesture {
				case "Enter":
					d.quickEnterPlain()
				case "Command-S":
					if !d.handleQuickPanelKey(events.KeyEvent{Key: events.KeyS, Modifiers: events.ModifierCommand}) {
						t.Fatal("save shortcut not handled")
					}
				case "Command-Return":
					d.quickEnterWithCommand()
				}
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
				for d.quickInput != "" || d.loading {
					select {
					case publish := <-app.posted:
						publish()
					case <-deadline.C:
						t.Fatalf("save stalled: %s", d.status)
					}
				}
				page, err := store.ListNoteDocumentsPage(context.Background(), notes.DocumentPageRequest{Limit: 2, MaxBytes: 2 << 20})
				if err != nil || len(page.Documents) != 1 || page.Documents[0].Text != source.want {
					t.Fatalf("pasted note bytes changed: notes=%d err=%v", len(page.Documents), err)
				}
			})
		}
	}
}
