package client

import (
	"fmt"
	"testing"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/platform/events"
	"github.com/hazcod/blur/internal/assistant"
)

func TestQuickPanelTypingHidesHistoryUntilDownWithoutReplacingDraft(t *testing.T) {
	d := &desktop{quickInput: "new unfinished question", quickConversation: []quickConversationTurn{
		{question: "Earlier question", answer: "Earlier answer"},
		{question: "Latest question", answer: "Latest answer"},
	}}
	if rows := d.quickTranscriptRows(); len(rows) != 0 {
		t.Fatal("typing still shows previous questions")
	}
	if !d.handleQuickPanelKey(events.KeyEvent{Key: events.KeyArrowDown}) {
		t.Fatal("Down did not reveal history while preserving the draft")
	}
	if d.quickInput != "new unfinished question" || len(d.quickTranscriptRows()) == 0 {
		t.Fatal("history reveal replaced the draft or left history hidden")
	}
	d.quickUI.copied = true
	d.setQuickTypedInput("another draft")
	if len(d.quickTranscriptRows()) != 0 || d.quickSelectedTurnIndex() >= 0 || d.quickPanelBodyEstimate() != 0 || d.quickPanelHairline() {
		t.Fatal("typing did not close history and return to the compact composer")
	}
}

func TestQuickPanelCanBrowseTwentyRecentAnswers(t *testing.T) {
	d := &desktop{}
	for n := 0; n < 25; n++ {
		d.quickConversation = appendQuickAction(d.quickConversation, fmt.Sprintf("Question %d", n), fmt.Sprintf("Answer %d", n))
	}
	if len(d.quickConversation) != 20 || d.quickConversation[0].question != "Question 5" {
		t.Fatalf("history retained %d entries; expected newest twenty", len(d.quickConversation))
	}
	for n := 24; n >= 5; n-- {
		if !d.handleQuickPanelKey(events.KeyEvent{Key: events.KeyArrowDown}) {
			t.Fatalf("cannot navigate to answer %d", n)
		}
		if got, ok := d.quickCopyText(); !ok || got != fmt.Sprintf("Answer %d", n) {
			t.Fatalf("selected %q; expected Answer %d", got, n)
		}
	}
}

func TestQuickPanelArrowsSelectAnswersWithoutReplacingComposer(t *testing.T) {
	d := &desktop{quickConversation: []quickConversationTurn{
		{question: "First question", answer: "First answer"},
		{question: "Second question", answer: "Second answer"},
	}}
	d.quickHistory.Record("Second question")
	for _, step := range []struct {
		key  events.Key
		want string
	}{
		{events.KeyArrowDown, "Second answer"},
		{events.KeyArrowDown, "First answer"},
		{events.KeyArrowDown, "First answer"},
		{events.KeyArrowUp, "Second answer"},
		{events.KeyArrowUp, "Second answer"},
	} {
		if !d.handleQuickPanelKey(events.KeyEvent{Key: step.key}) {
			t.Fatal("answer navigation key was left to the text editor")
		}
		if answer, ok := d.quickCopyText(); !ok || answer != step.want || d.quickInput != "" {
			t.Fatalf("selected answer=%q available=%t composer=%q, want %q and empty composer", answer, ok, d.quickInput, step.want)
		}
	}
}

func TestQuickPanelArrowsPassRepeatedIdenticalAnswers(t *testing.T) {
	d := &desktop{quickConversation: []quickConversationTurn{
		{question: "Earlier question", answer: "Earlier answer"},
		{question: "What was my video about?", answer: "The same retrieved passage"},
		{question: "What was my video about?", answer: "The same retrieved passage"},
	}}
	for _, step := range []struct {
		key  events.Key
		want int
	}{
		{events.KeyArrowDown, 2},
		{events.KeyArrowDown, 1},
		{events.KeyArrowDown, 0},
		{events.KeyArrowDown, 0},
		{events.KeyArrowUp, 1},
		{events.KeyArrowUp, 2},
	} {
		if !d.handleQuickPanelKey(events.KeyEvent{Key: step.key}) || d.quickSelectedTurnIndex() != step.want {
			t.Fatalf("key %v selected row %d, want %d; identical answers must remain separate history entries", step.key, d.quickSelectedTurnIndex(), step.want)
		}
	}
}

func TestQuickPanelSelectedTurnSurvivesHistoryTrimWithoutMatchingAnotherCopy(t *testing.T) {
	d := &desktop{}
	for range quickConversationLimit {
		d.quickConversation = appendQuickAction(d.quickConversation, "2+2", "4")
	}
	d.selectQuickTurn(1)
	d.quickConversation = appendQuickAction(d.quickConversation, "2+2", "4")
	if got := d.quickSelectedTurnIndex(); got != 0 {
		t.Fatalf("bounded append moved the selection to row %d, want the same surviving turn at 0", got)
	}
	d.quickConversation = appendQuickAction(d.quickConversation, "2+2", "4")
	if got := d.quickSelectedTurnIndex(); got != -1 {
		t.Fatalf("evicted selection matched a different identical turn at row %d", got)
	}
}

func TestQuickPanelDraftKeepsCopyAndHistoryBehavior(t *testing.T) {
	d := &desktop{quickInput: "unfinished draft"}
	d.quickHistory.Record("Earlier input")
	if d.handleQuickPanelKey(events.KeyEvent{Key: events.KeyC, Modifiers: events.ModifierCommand}) {
		t.Fatal("Command-C intercepted the composer's draft copy")
	}
	if !d.handleQuickPanelKey(events.KeyEvent{Key: events.KeyArrowUp}) || d.quickInput != "Earlier input" {
		t.Fatalf("Up did not recall local history: %q", d.quickInput)
	}
	if !d.handleQuickPanelKey(events.KeyEvent{Key: events.KeyArrowDown}) || d.quickInput != "unfinished draft" {
		t.Fatalf("Down did not restore the draft: %q", d.quickInput)
	}
	if d.handleQuickPanelKey(events.KeyEvent{Key: events.KeyArrowUp, Modifiers: events.ModifierShift}) {
		t.Fatal("Shift-Up intercepted normal editor selection")
	}
}

func TestQuickPanelCopyDefaultsToLatestAnswer(t *testing.T) {
	d := &desktop{quickConversation: []quickConversationTurn{{answer: "Earlier"}, {answer: "Latest"}}}
	if answer, ok := d.quickCopyText(); !ok || answer != "Latest" {
		t.Fatalf("copy=%q available=%t, want latest answer", answer, ok)
	}
}

func TestQuickPanelDismissalHidesHistoryAndDisablesHiddenAnswerShortcuts(t *testing.T) {
	d := &desktop{quickConversation: []quickConversationTurn{{question: "421-12", answer: "409"}}}
	d.navigateQuickPanel(1)
	d.collapseTransientContent("quick")
	if len(d.quickVisibleTurns()) != 0 || d.quickPanelExpanded() {
		t.Fatal("dismissed conversation remained visible on reopening")
	}
	if _, ok := d.quickCopyText(); ok || d.quickNoteSaveAvailable() {
		t.Fatal("hidden answer still intercepts copy/save shortcuts")
	}
	if !d.navigateQuickPanel(1) || len(d.quickVisibleTurns()) != 1 {
		t.Fatal("Down cannot recover the retained history after dismissal")
	}
	if text, ok := d.quickCopyText(); !ok || text != "409" {
		t.Fatal("revealed answer cannot be copied")
	}
}

func TestQuickPanelHistoryKeepsModelContextBounded(t *testing.T) {
	d := &desktop{}
	for n := 0; n < 25; n++ {
		d.quickConversation = appendQuickConversation(d.quickConversation, fmt.Sprintf("Question %d", n), assistant.Result{General: true, Answer: assistant.Answer{Text: fmt.Sprintf("Answer %d", n)}})
	}
	d.navigateQuickPanel(1)
	if visible := d.quickVisibleTurns(); len(visible) != 20 || visible[0] != 19 || visible[19] != 0 {
		t.Fatalf("history is not the newest twenty in reverse order: %v", visible)
	}
	if history := quickHistoryTurns(d.quickConversation); len(history) != 4 || history[0].Question != "Question 21" || history[3].Question != "Question 24" {
		t.Fatalf("UI history expanded model context: %+v", history)
	}
}

func TestCommandSSavesDraftOrSelectedAnswerAsExplicitNote(t *testing.T) {
	for _, tc := range []struct {
		input string
		turns []quickConversationTurn
		want  string
	}{
		{input: "421-12", want: "421-12"},
		{turns: []quickConversationTurn{{question: "421-12", answer: "409", general: true}}, want: "421-12\n\n409"},
	} {
		d := &desktop{quickInput: tc.input, quickConversation: tc.turns}
		saved, calls := "", 0
		d.quickUI.saveNoteFn = func() { saved = d.quickInput; calls++ }
		if !d.handleQuickPanelKey(events.KeyEvent{Key: events.KeyS, Modifiers: events.ModifierCommand}) || saved != tc.want || calls != 1 {
			t.Fatalf("Command-S did not explicitly save %q: saved=%q calls=%d", tc.want, saved, calls)
		}
	}
}

func TestQuickPanelForcedNotesPreserveOriginalPaste(t *testing.T) {
	for _, input := range []string{
		"  Meeting: rollback review\nAttendees: Mira, Noor\nDecision: wait for approval.\n\n",
		"\n\tWhat did Mira say?\nKeep the trailing whitespace.  \n",
	} {
		d := &desktop{quickInput: input}
		if text := quickPanelNoteText(input); text != input {
			t.Fatal("forced note changed pasted source bytes")
		}
		var saved string
		d.quickUI.saveNoteFn = func() { saved = d.quickInput }
		d.handleQuickPanelKey(events.KeyEvent{Key: events.KeyS, Modifiers: events.ModifierCommand})
		if saved != input {
			t.Fatal("Command-S changed pasted source bytes")
		}
	}
}

func TestQuickPanelKeyboardCapturesBeforeFocusedEditor(t *testing.T) {
	d := &desktop{quickConversation: []quickConversationTurn{{answer: "Latest"}}}
	controller := d.newQuickPanelKeyController()
	if controller.Phase() != gui.PhaseCapture {
		t.Fatal("popup navigation runs after the text editor")
	}
	handled := false
	ctx := &quickPanelKeyContext{event: events.KeyEvent{EventType: events.KeyDown, Key: events.KeyArrowDown, Handled: &handled}}
	controller.HandleEvent(ctx)
	if !ctx.PropagationStopped() || d.quickSelectedTurnIndex() != 0 {
		t.Fatal("popup did not consume navigation before editor dispatch")
	}
	if !handled {
		t.Fatal("native editor can still perform its default navigation")
	}
}

type quickPanelKeyContext struct {
	gui.EventContext
	event   events.Event
	stopped bool
}

func (c *quickPanelKeyContext) Event() events.Event      { return c.event }
func (c *quickPanelKeyContext) StopPropagation()         { c.stopped = true }
func (c *quickPanelKeyContext) PropagationStopped() bool { return c.stopped }

func TestQuickPanelActivationPreferenceSurvivesFocusLoss(t *testing.T) {
	d := &desktop{dashboardOpen: true}
	d.quickUI.preferQuick = true
	if got := desktopWindowOnActivation(d.dashboardOpen, d.quickActivationPreferred()); got != "quick" {
		t.Fatalf("activation selected %q, want quick after panel focus loss", got)
	}
	d.quickUI.preferQuick = false
	if got := desktopWindowOnActivation(d.dashboardOpen, d.quickActivationPreferred()); got != "main" {
		t.Fatalf("explicit dashboard activation selected %q, want main", got)
	}
}

func TestClosedDashboardDoesNotReopenSettingsOnActivation(t *testing.T) {
	d := &desktop{app: &quickDashboardApp{}, dashboardOpen: true, page: "Settings"}
	d.closeDashboard()
	if d.dashboardOpen || desktopWindowOnActivation(d.dashboardOpen, d.quickActivationPreferred()) != "" {
		t.Fatal("closed settings remain the app activation target")
	}
}

func TestEscapeCancelsPendingInterpretationBeforeClosing(t *testing.T) {
	canceled := false
	d := &desktop{app: &quickDashboardApp{}, quickPanelPinned: true, quickEntryLoading: true, loading: true}
	d.quickEntryCancel = func() { canceled = true }
	d.handleQuickPanelEscape()
	if !canceled || d.quickEntryLoading || d.loading || d.quickPanelPinned {
		t.Fatal("Escape left request interpretation running after closing the popup")
	}
}

func TestExplicitSettingsChoiceOverridesQuickPanelActivation(t *testing.T) {
	d := &desktop{app: &quickDashboardApp{}, quickPanelPinned: true, quickPreview: true, powerStatus: "checked"}
	d.quickUI.preferQuick = true
	d.openSettingsFromMenu()
	if !d.dashboardOpen || d.page != "Settings" || d.quickActivationPreferred() {
		t.Fatal("explicit Settings choice kept the popup eligible to steal activation")
	}
}

func TestQuickSelectionScrollCoordinatesIncludeNestedQuestion(t *testing.T) {
	content := &quickPanelRectWidget{}
	button := &quickPanelRectWidget{parent: content, rect: geometry.Rect(4, 180, 400, 24)}
	label := &quickPanelRectWidget{parent: button, rect: geometry.Rect(6, 3, 200, 18)}
	if got, ok := quickRectInAncestor(label, content); !ok || got != geometry.Rect(10, 183, 200, 18) {
		t.Fatalf("question scroll bounds=%+v found=%t, want content coordinates", got, ok)
	}
	if _, ok := quickRectInAncestor(label, &quickPanelRectWidget{}); ok {
		t.Fatal("unrelated widget was treated as part of the popup scroll content")
	}
}

type quickPanelRectWidget struct {
	gui.Widget
	parent gui.Widget
	rect   geometry.Rectangle
}

func (w *quickPanelRectWidget) Parent() gui.Widget       { return w.parent }
func (w *quickPanelRectWidget) Rect() geometry.Rectangle { return w.rect }
