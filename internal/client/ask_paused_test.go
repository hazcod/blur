package client

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// Only the GUI event dispatcher is replaced. Retrieval and persistence use a
// temporary encrypted database; capture and all model/network actions stay off.
type pausedAskApp struct {
	ui.App
	posted chan func()
}

func (*pausedAskApp) RequestUpdate()                              {}
func (*pausedAskApp) FindWindow(string) gui.Window                { return nil }
func (*pausedAskApp) TimeoutFunc(time.Duration, func()) *ui.Timer { return nil }
func (a *pausedAskApp) Post(f func())                             { a.posted <- f }

func TestSavedEncryptedNoteCanBeAskedWhileCaptureIsDisabled(t *testing.T) {
	for _, surface := range []string{"client", "dashboard", "popup"} {
		t.Run(surface, func(t *testing.T) {
			store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func(close func() error) { _ = close() }(store.Close)
			c := NewClient(store).WithNotes(notes.NewService(store, nil, nil))
			defer c.Close()
			c.SetCaptureStateReader(func() (bool, time.Time) { return false, time.Now().Add(-time.Hour) })
			note, err := c.AddNote("The rollback owner is Mira.")
			if err != nil {
				t.Fatal(err)
			}
			if !c.CanAskMemory() {
				t.Fatal("paused recording hid stored evidence")
			}
			const question = "Who is Mira?"
			var answer *assistant.Result
			if surface == "client" {
				got, err := c.AskMemory(context.Background(), question)
				if err != nil {
					t.Fatal(err)
				}
				answer = &got
			} else {
				app := &pausedAskApp{posted: make(chan func(), 16)}
				d := &desktop{app: app, client: c, captureState: "paused", askQuestion: question, quickInput: question}
				d.preferences.CaptureEnabled = false
				if surface == "dashboard" {
					d.askMemory()
				} else {
					d.submitQuickPanelInput()
				}
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
				for d.askResult == nil {
					select {
					case publish := <-app.posted:
						publish()
					case <-deadline.C:
						t.Fatalf("%s submission stalled with capture disabled: %q", surface, d.status)
					}
				}
				answer = d.askResult
				if d.preferences.CaptureEnabled {
					t.Fatal("Ask enabled capture")
				}
			}
			if !answer.Direct || !strings.Contains(answer.Answer.Text, "The rollback owner is Mira.") || len(answer.Sources) != 1 || answer.Sources[0].SourceID != note.SourceID || answer.SentItems != 0 || answer.SentBytes != 0 {
				t.Fatalf("paused saved-note recall failed: %#v", answer)
			}
		})
	}
}

func TestCaptureSetupCannotBlockOpeningSavedMemoryAsk(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		d := &desktop{app: &pausedAskApp{posted: make(chan func(), 8)}, setupRequired: true, quickPanelPinned: true,
			quickConversation: []quickConversationTurn{{question: "Who is Mira?", answer: "The rollback owner is Mira."}}}
		d.preferences.CaptureEnabled = enabled
		d.openDashboard()
		if !d.dashboardOpen || d.quickPanelPinned || d.page != "Ask" || d.askQuestion != "Who is Mira?" {
			t.Fatalf("capture setup blocked saved-memory Ask (capture enabled=%v)", enabled)
		}
		if d.preferences.CaptureEnabled != enabled {
			t.Fatal("opening Ask changed recording consent")
		}
	}
}
