package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/platform/events"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/tasks"
)

func finishMemoryEntry(t *testing.T, app *meetingOperationTestApp, d *desktop) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for d.loading || d.askLoading || d.memoryUI.entryLoading || d.quickEntryLoading {
		select {
		case receipt := <-app.posted:
			receipt()
		case <-deadline.C:
			t.Fatal("entry did not complete", d.status)
		}
	}
}

func TestMemoryComposerAndPopupCreateTheSameExplicitNotesAndReminders(t *testing.T) {
	for _, surface := range []string{"dashboard", "popup"} {
		t.Run(surface, func(t *testing.T) {
			c, store, _ := todayTaskClient(t)
			app := &meetingOperationTestApp{posted: make(chan func(), 64)}
			d := &desktop{app: app, client: c, page: "Ask"}
			submit := func(input string) {
				if surface == "dashboard" {
					d.askQuestion = input
					d.submitMemoryQuery()
				} else {
					d.quickInput = input
					d.submitQuickPanelInput()
				}
				finishMemoryEntry(t, app, d)
			}
			const note = "Project Aurora now uses port 4317."
			submit("save a note: " + note)
			storedNotes, err := store.ListNoteDocumentsPage(context.Background(), notes.DocumentPageRequest{Limit: 12, MaxBytes: 512 << 10})
			if err != nil || len(storedNotes.Documents) != 1 || storedNotes.Documents[0].Text != note {
				t.Fatal("explicit note did not persist through common composer", surface, d.status, err)
			}
			saved, found, err := store.GetObservation(context.Background(), storedNotes.Documents[0].SourceID)
			if err != nil || !found || saved.Text() != note {
				t.Fatal("note document did not retain genuine saved source", err)
			}

			submit("remind me tomorrow at 9am to check backups")
			page, err := store.ListTasks(context.Background(), tasks.PageRequest{Limit: 20})
			if err != nil || len(page.Items) != 1 || page.Items[0].Title != "check backups" || page.Items[0].Reminder == nil || page.Items[0].Reminder.DueAt.IsZero() {
				t.Fatal("explicit reminder did not persist through common composer", surface, d.status, page, err)
			}
			if surface == "dashboard" && (d.quickPanelPinned || d.quickAskPending || d.quickNotePending || len(d.quickConversation) != 0) {
				t.Fatal("dashboard action opened or changed popup conversation")
			}
		})
	}
}

func TestAskSurfacesKeepProjectOpenWorkAwayFromAnUnrelatedLoginCapture(t *testing.T) {
	for _, surface := range []string{"dashboard", "popup"} {
		t.Run(surface, func(t *testing.T) {
			c, store, now := todayTaskClient(t)
			login, err := capture.SanitizeObservation("com.example.Browser", "accounts.craftedsignal.io", "Sign in to your account. Email address. Remember me. Protected by reCAPTCHA.", 4096)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.PutObservationAt(context.Background(), login, now); err != nil {
				t.Fatal(err)
			}
			app := &meetingOperationTestApp{posted: make(chan func(), 64)}
			d := &desktop{app: app, client: c, page: "Ask"}
			const question = "what's outstanding for craftedsignal?"
			if surface == "dashboard" {
				d.askQuestion = question
				d.submitMemoryQuery()
			} else {
				d.quickInput = question
				d.submitQuickPanelInput()
			}
			finishMemoryEntry(t, app, d)
			if d.askResult == nil || !d.askResult.Direct || !d.askResult.Answer.InsufficientEvidence || len(d.askResult.Sources) != 0 || strings.Contains(strings.ToLower(d.askResult.Answer.Text), "sign in") || !strings.Contains(d.askResult.Answer.Text, "saved open work") {
				t.Fatalf("%s Ask surfaced an unrelated project login page: result=%+v status=%q", surface, d.askResult, d.status)
			}
		})
	}
}

func TestMemoryComposerCalculationCopiesAndSaveShortcutKeepsNoteAuthority(t *testing.T) {
	c, store, _ := todayTaskClient(t)
	app := &meetingOperationTestApp{posted: make(chan func(), 64)}
	d := &desktop{app: app, client: c, page: "Ask", askQuestion: "20*7", quickInput: "untouched popup draft"}
	d.submitMemoryQuery()
	finishMemoryEntry(t, app, d)
	if d.askResult == nil || d.askResult.Answer.Text != "140" || d.askQuestion != "" || len(d.askConversation) != 1 {
		t.Fatal("calculation was not answered locally", d.status, d.askResult)
	}
	var copied string
	d.memoryUI.copyText = func(text string) error { copied = text; return nil }
	root, _, _ := memoryWidgetTree(t, d)
	root.AddEventController(d.newMemoryKeyController())
	var input gui.Widget
	var walk func(gui.Widget)
	walk = func(w gui.Widget) {
		if _, ok := w.(*gui.TextInput); ok {
			input = w
		}
		for _, child := range w.Children() {
			walk(child)
		}
	}
	walk(root)
	if input == nil {
		t.Fatal("dashboard composer was not mounted")
	}
	host := &memoryKeyHost{root: root, focused: input}
	handled := false
	if err := new(gui.EventDispatcher).DispatchEvent(host, events.KeyEvent{EventType: events.KeyDown, Key: events.KeyC, Modifiers: events.ModifierCommand, Handled: &handled}); err != nil {
		t.Fatal(err)
	}
	if !handled || copied != "140" {
		t.Fatal("Command-C did not copy the calculation answer", copied)
	}
	d.askQuestion = "20*7"
	if d.handleMemoryKey(events.KeyEvent{Key: events.KeyC, Modifiers: events.ModifierCommand}) {
		t.Fatal("answer copy intercepted an unsent draft")
	}
	if !d.handleMemoryKey(events.KeyEvent{Key: events.KeyS, Modifiers: events.ModifierCommand}) {
		t.Fatal("Command-S note override was unavailable")
	}
	finishMemoryEntry(t, app, d)
	storedNotes, err := store.ListNoteDocumentsPage(context.Background(), notes.DocumentPageRequest{Limit: 12, MaxBytes: 512 << 10})
	if err != nil || len(storedNotes.Documents) != 1 || storedNotes.Documents[0].Text != "20*7" {
		t.Fatal("Command-S calculation became an answer instead of a note", err)
	}

	if d.quickInput != "untouched popup draft" || d.quickPanelPinned {
		t.Fatal("dashboard shortcuts changed popup draft or focus")
	}
}

type memoryKeyHost struct{ root, focused gui.Widget }

func (h *memoryKeyHost) Widget() gui.Widget                 { return h.root }
func (h *memoryKeyHost) FocusedWidget() gui.Widget          { return h.focused }
func (h *memoryKeyHost) SetFocusedWidget(w gui.Widget) bool { h.focused = w; return true }

func TestMemoryComposerSharesContextualFollowupsAndReadRequestsWithoutIntentModel(t *testing.T) {
	g := &singleSearchModel{respond: func(context.Context, string, string) (string, error) {
		t.Fatal("clear read request invoked intent inference")
		return "", nil
	}}
	c := selectedEntryClient(t, ProviderApple, false, g)
	epoch, _ := c.BeginMemoryRead()
	for _, input := range []string{"tell me about Mira", "and his email", "make it shorter"} {
		state := EntryState{Epoch: epoch, HasAnswer: true}
		popup, err := c.ResolveEntry(context.Background(), input, state, "UTC")
		if err != nil {
			t.Fatal(err)
		}
		main, err := c.resolveMemoryEntry(context.Background(), input, state, "UTC")
		if err != nil || main.Action.Kind != intent.Ask || main.SearchQuery != "" || main.Action.Text != input || popup.Action.Kind != main.Action.Kind {
			t.Fatal("dashboard lost shared read/follow-up routing", input, popup, main, err)
		}
	}
	for _, input := range []string{"rollback owner", "compiler examples"} {
		resolved, err := c.resolveMemoryEntry(context.Background(), input, EntryState{}, "UTC")
		if err != nil || resolved.SearchQuery != input || resolved.Action.Kind != "" {
			t.Fatal("plain topic did not retain the cheap read-only source-search route", resolved, err)
		}
	}
}

type memoryEntryAnswerModel struct {
	calls  int
	packed assistant.PackedContext
}

func (*memoryEntryAnswerModel) CloseIdleConnections() {}

func (*memoryEntryAnswerModel) CountInputTokens(context.Context, string, string) (int, error) {
	return 100, nil
}
func (m *memoryEntryAnswerModel) Generate(_ context.Context, p assistant.PackedContext, _ int) (assistant.ModelAnswer, error) {
	m.calls++
	m.packed = p
	return assistant.ModelAnswer{Text: "Mira leads Aurora. His email is mira@example.org.", SourceIDs: append([]string(nil), p.SourceIDs...)}, nil
}

func TestMemoryComposerFollowupRetainsGenuineSourcesAndDashboardHistory(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	note, err := c.AddNote("Mira leads Aurora. His email is mira@example.org.")
	if err != nil {
		t.Fatal(err)
	}
	model := &memoryEntryAnswerModel{}
	c.localAI = &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}, askModel: model}
	app := &meetingOperationTestApp{posted: make(chan func(), 64)}
	d := &desktop{app: app, client: c, page: "Ask", askQuestion: "Who is Mira?"}
	d.submitMemoryQuery()
	finishMemoryEntry(t, app, d)
	d.askQuestion = "and his email"
	d.submitMemoryQuery()
	finishMemoryEntry(t, app, d)
	if d.askResult == nil || len(d.askConversation) != 2 || d.askResult.General || d.askResult.Plan.Entity != "Mira" || len(d.askResult.Sources) != 1 || d.askResult.Sources[0].SourceID != note.SourceID || !strings.Contains(d.askResult.Answer.Text, "mira@example.org") {
		t.Fatal("pronoun follow-up lost genuine memory/history", d.status, d.askResult)
	}
	d.askQuestion = "make it shorter"
	d.submitMemoryQuery()
	finishMemoryEntry(t, app, d)
	if d.askResult == nil || len(d.askConversation) != 3 || d.askResult.General || d.askResult.Plan.Entity != "Mira" || len(d.quickConversation) != 0 {
		t.Fatal("answer transformation escaped dashboard history", d.status, d.askResult)
	}
}
