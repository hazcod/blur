package client

import (
	"image/color"
	"reflect"
	"testing"
	"time"

	"github.com/golang-gui/goui/gui"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/today"
)

type quickPanelFocusWidget struct{ gui.Widget }

type quickPanelFocusWindowStub struct {
	focused gui.Widget
	allow   bool
}

func TestQuickPreviewUsesConciseChronicleRecapsInsteadOfAppActivity(t *testing.T) {
	start := time.Date(2026, 11, 8, 10, 0, 0, 0, time.Local)
	items := quickPreviewRecaps([]chronicle.Summary{
		{HourID: "hour:20261108T09Z", Start: start.Add(-time.Hour), Text: "Reviewed the launch plan. Anna will confirm the budget tomorrow. Additional detail that should not take over the compact preview."},
		{HourID: "hour:20261108T10Z", Start: start, Text: "Waiting for final screenshots"},
	})
	if len(items) != 2 {
		t.Fatalf("quick preview recaps = %#v, want two summaries", items)
	}
	if items[0] != "Waiting for final screenshots" || items[1] != "Reviewed the launch plan." {
		t.Fatalf("quick preview recaps = %#v, want latest-first concise summaries", items)
	}
	if got := quickPreviewRecaps(nil); len(got) != 0 {
		t.Fatalf("missing recaps = %#v, want no fabricated activity", got)
	}
}

func TestQuickPreviewRejectsAppOnlySubjectAndRecentActivityAsRecap(t *testing.T) {
	snapshot := today.Snapshot{
		ActiveSubjects: []today.Subject{{Label: "Slack"}},
		RecentChanges:  []today.Change{{Text: "Active in Slack"}},
	}
	if quickPreviewHasUsefulContent(snapshot, nil) {
		t.Fatal("app-name activity was treated as a useful recap")
	}
	snapshot.RecentChanges = []today.Change{{Text: "Budget approved after finance review."}}
	if !quickPreviewHasUsefulContent(snapshot, nil) {
		t.Fatal("meaningful captured change was not treated as recap content")
	}
	if got := previewChangeTexts([]today.Change{{Text: "Slack"}, {Text: "Active in Gmail"}}); len(got) != 0 {
		t.Fatalf("app-only highlights = %#v, want none", got)
	}
	if got := previewChangeTexts([]today.Change{{Text: "my own question", Origin: today.ChangeOriginUser}}); len(got) != 0 {
		t.Fatalf("user prompt appeared as a Highlight: %#v", got)
	}
	snapshot.OpenLoops = []today.OpenLoop{{Text: "Confirm the launch date"}}
	if !quickPreviewHasUsefulContent(snapshot, nil) {
		t.Fatal("an open item was not treated as useful recap content")
	}
}

func TestTodayRecapDoesNotCountAppActivityAsUsefulItems(t *testing.T) {
	snapshot := today.Snapshot{
		ActiveSubjects: []today.Subject{{Label: "Slack"}},
		RecentChanges:  []today.Change{{Text: "Active in Slack"}},
	}
	if todayHasUsefulItems(snapshot) {
		t.Fatal("app-name activity was treated as a useful Today recap")
	}
	snapshot.Questions = []today.Question{{Text: "Who owns the follow-up?"}}
	if !todayHasUsefulItems(snapshot) {
		t.Fatal("an unresolved follow-up was not treated as a useful Today item")
	}
}

type quickPanelBackgroundWindowStub struct{ styleName string }

func (w *quickPanelBackgroundWindowStub) SetStyleName(name string) { w.styleName = name }

func TestQuickPanelSetsOpaqueHostBackground(t *testing.T) {
	window := &quickPanelBackgroundWindowStub{}
	setQuickPanelHostBackground(window)
	if window.styleName != "interact-background" {
		t.Fatalf("quick panel host style = %q, want opaque interact background", window.styleName)
	}
	setQuickPanelHostBackground(nil)
}

func TestQuickPanelDismissesAfterLosingFocusOnlyWhenVisible(t *testing.T) {
	for _, test := range []struct {
		name    string
		visible bool
		focused bool
		want    bool
	}{
		{"click away from visible popup", true, false, true},
		{"focused popup", true, true, false},
		{"already hidden popup", false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := quickPanelShouldDismissAfterFocusChange(test.visible, test.focused); got != test.want {
				t.Fatalf("quickPanelShouldDismissAfterFocusChange(%t, %t) = %t, want %t", test.visible, test.focused, got, test.want)
			}
		})
	}
}

func TestQuickPanelFocusDismissalHookDismissesVisiblePanel(t *testing.T) {
	var focusChanged func(bool)
	visible, dismissals := true, 0
	if !connectQuickPanelFocusDismissal(func(handler func(bool)) { focusChanged = handler }, func() bool { return visible }, func() {
		dismissals++
		visible = false
	}) {
		t.Fatal("focus dismissal hook was not connected")
	}
	focusChanged(false)
	if dismissals != 1 || visible {
		t.Fatalf("click-away focus change left popup visible=%t dismissals=%d", visible, dismissals)
	}
	focusChanged(false)
	if dismissals != 1 {
		t.Fatalf("hidden popup was dismissed again %d times", dismissals)
	}
}

func (w *quickPanelFocusWindowStub) SetFocusedWidget(widget gui.Widget) bool {
	if w.allow {
		w.focused = widget
	}
	return w.allow
}

func TestQuickPanelFocusTargetsOnlyMountedComposer(t *testing.T) {
	widget := &quickPanelFocusWidget{}
	window := &quickPanelFocusWindowStub{allow: true}
	if !focusQuickPanelComposer(window, widget) {
		t.Fatal("focusQuickPanelComposer() failed for a mounted composer")
	}
	if window.focused != widget {
		t.Fatal("Quick Capture focused a different widget")
	}
	if focusQuickPanelComposer(window, nil) {
		t.Fatal("focusQuickPanelComposer() succeeded without a mounted composer")
	}
	if focusQuickPanelComposer(nil, widget) {
		t.Fatal("focusQuickPanelComposer() succeeded without a mounted window")
	}
	window.allow = false
	if focusQuickPanelComposer(window, widget) {
		t.Fatal("focusQuickPanelComposer() reported focus when the window rejected it")
	}
}

func TestQuickPanelFocusingFollowsHoverAndNotchClick(t *testing.T) {
	for _, test := range []struct {
		name    string
		hovered bool
		pinned  bool
		want    bool
	}{
		{"hover preview", true, false, true},
		{"notch click", false, true, true},
		{"hidden", false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			window := &quickPanelFocusWindowStub{allow: true}
			input := &quickPanelFocusWidget{}
			got := focusQuickPanelInput(test.hovered, test.pinned, window, input)
			if got != test.want {
				t.Fatalf("focusQuickPanelInput(%t, %t) = %t, want %t", test.hovered, test.pinned, got, test.want)
			}
			if got && window.focused != input {
				t.Fatal("visible Interact did not focus its text input")
			}
		})
	}
}

func TestQuickPanelLeavesRecapPreviewForAskAndStatus(t *testing.T) {
	for _, test := range []struct {
		name                              string
		preview, pinned, expanded, status bool
		wantPreview                       bool
	}{
		{"plain hover", true, false, false, false, true},
		{"pinned composer", true, true, false, false, false},
		{"pending ask", true, false, true, false, false},
		{"answer available", true, false, true, false, false},
		{"submission status", true, false, false, true, false},
		{"not previewing", false, false, false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := quickPanelShowsDayPreview(test.preview, test.pinned, test.expanded, test.status); got != test.wantPreview {
				t.Fatalf("quickPanelShowsDayPreview(...) = %t, want %t", got, test.wantPreview)
			}
		})
	}
}

func TestQuickPanelSubmitsQuestionsAsMemoryQueries(t *testing.T) {
	for _, input := range []string{"What did we decide?", "what did we decide", "what is 2+2", "  ask: where is the draft  ", "Give me a recap", "please summarise today", "what's new"} {
		if !quickPanelWantsAnswer(input) {
			t.Errorf("quickPanelWantsAnswer(%q) = false, want true", input)
		}
	}
	for _, input := range []string{"Call Sam about the launch", "Remember the project deadline", "Call Anna tomorrow", "Idea: try the new API"} {
		if quickPanelWantsAnswer(input) {
			t.Errorf("quickPanelWantsAnswer(%q) = true, want false", input)
		}
	}
}

func TestQuickConversationMarksGeneralAnswerAsNotFromSavedMemory(t *testing.T) {
	turns := appendQuickConversation(nil, "What is 2+2?", assistant.Result{General: true, Answer: assistant.Answer{Text: "4"}})
	if len(turns) != 1 || !turns[0].general || turns[0].answer != "4" || turns[0].sources != 0 {
		t.Fatalf("general answer presentation metadata = %+v", turns)
	}
}

func TestQuickPanelHighlightsExcludeUserNotesAndKeepCapturedActivity(t *testing.T) {
	items := previewChangeTexts([]today.Change{
		{Text: "what is 2+2", Origin: today.ChangeOriginUser},
		{Text: "Finance approved the Q3 budget", Origin: today.ChangeOriginActivity},
	})
	if len(items) != 1 || items[0] != "Finance approved the Q3 budget" {
		t.Fatalf("Highlights = %#v, want only captured activity", items)
	}
}

func TestQuickPanelPointerTransitionArmsAndDismissesOnExit(t *testing.T) {
	for _, test := range []struct {
		name                   string
		visible, armed, inside bool
		wantArmed, wantDismiss bool
	}{
		{"enter", true, false, true, true, false},
		{"leave visible", true, true, false, false, true},
		{"outside before enter", true, false, false, false, false},
		{"hidden", false, true, false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			armed, dismiss := quickPanelPointerTransition(test.visible, test.armed, test.inside)
			if armed != test.wantArmed || dismiss != test.wantDismiss {
				t.Fatalf("transition = (armed=%t, dismiss=%t), want (%t, %t)", armed, dismiss, test.wantArmed, test.wantDismiss)
			}
		})
	}
}

func TestQuickCaptureHistoryRecallsAndRestoresDraft(t *testing.T) {
	var history quickEntryHistory
	history.Record("Call Mira about the launch")
	history.Record("Send the notes")
	if got, ok := history.Previous("unfinished draft"); !ok || got != "Send the notes" {
		t.Fatalf("first Up = %q, %t; want newest entry", got, ok)
	}
	if got, ok := history.Previous("Send the notes"); !ok || got != "Call Mira about the launch" {
		t.Fatalf("second Up = %q, %t; want older entry", got, ok)
	}
	if got, ok := history.Next(); !ok || got != "Send the notes" {
		t.Fatalf("first Down = %q, %t; want newer entry", got, ok)
	}
	if got, ok := history.Next(); !ok || got != "unfinished draft" {
		t.Fatalf("last Down = %q, %t; want original draft", got, ok)
	}
}

func TestQuickCaptureTabCompletesOnlyFromRecentLocalEntries(t *testing.T) {
	var history quickEntryHistory
	history.Record("Send the launch notes")
	history.Record("Schedule the review")
	if got, ok := history.Complete("send the"); !ok || got != "Send the launch notes" {
		t.Fatalf("Tab completion = %q, %t; want the case-insensitive local prefix match", got, ok)
	}
	if got, ok := history.Complete("unknown"); ok || got != "" {
		t.Fatalf("unmatched Tab completion = %q, %t; want no completion", got, ok)
	}
}

func TestQuickCaptureHistoryRetainsOnlyRecentBoundedEntries(t *testing.T) {
	var history quickEntryHistory
	for i := 0; i < quickHistoryLimit+2; i++ {
		history.Record(string(rune('a' + i)))
	}
	if got, want := len(history.entries), quickHistoryLimit; got != want {
		t.Fatalf("history entries = %d, want cap %d", got, want)
	}
	if got, ok := history.Previous(""); !ok || got != string(rune('a'+quickHistoryLimit+1)) {
		t.Fatalf("newest retained entry = %q, %t", got, ok)
	}
}

func TestQuickPanelIsNotMountedAtLaunch(t *testing.T) {
	if got := quickPanelWindowIDs(false, false); len(got) != 0 {
		t.Fatalf("startup windows = %#v, want no window before user interaction", got)
	}
}

func TestQuickPanelWindowIDsFollowExplicitVisibilityState(t *testing.T) {
	tests := []struct {
		name             string
		dashboard, quick bool
		want             []string
	}{
		{"quick interaction", false, true, []string{"quick"}},
		{"dashboard", true, false, []string{"main"}},
		{"dashboard with quick", true, true, []string{"quick", "main"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := quickPanelWindowIDs(test.dashboard, test.quick); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("quickPanelWindowIDs(%t, %t) = %#v, want %#v", test.dashboard, test.quick, got, test.want)
			}
		})
	}
}

func TestSetupWizardTracksAccessibilityCapability(t *testing.T) {
	tests := []struct {
		name    string
		report  platform.CapabilityReport
		running bool
		want    bool
	}{
		{"permission required", platform.CapabilityReport{State: platform.PermissionRequired}, false, true},
		{"capture running", platform.CapabilityReport{State: platform.Available}, true, false},
		{"permission granted before worker starts", platform.CapabilityReport{State: platform.Available}, false, false},
		{"other capture failure", platform.CapabilityReport{State: platform.TemporarilyUnresponsive}, false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := captureSetupRequired(test.report, test.running); got != test.want {
				t.Fatalf("captureSetupRequired() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestAppReactivationChoosesAWindowToReopen(t *testing.T) {
	for _, test := range []struct {
		name      string
		dashboard bool
		want      string
	}{{"quick capture", false, "quick"}, {"dashboard", true, "main"}} {
		t.Run(test.name, func(t *testing.T) {
			var shown string
			rechecked := false
			if err := reopenDesktopOnActivation(test.dashboard, func(id string) error { shown = id; return nil }, func() { rechecked = true }); err != nil {
				t.Fatal(err)
			}
			if shown != test.want || !rechecked {
				t.Fatalf("activation showed %q and rechecked=%t, want %q and true", shown, rechecked, test.want)
			}
		})
	}
}

func TestQuickPanelHeightFitsContent(t *testing.T) {
	for _, test := range []struct {
		name     string
		inset    float32
		progress bool
		hairline bool
		body     float32
		want     int
	}{
		{"empty without notch", 0, false, false, 0, 10 + 36 + 10},
		{"empty under notch", 36, false, false, 0, 36 + 10 + 36 + 10},
		{"status line", 0, false, false, 16, 10 + 36 + 6 + 16 + 10},
		{"pending adds only the bar and a line", 0, true, false, 16, 10 + 36 + 6 + 2 + 6 + 16 + 10},
		{"short answer grows to fit", 0, false, true, 120, 10 + 36 + 6 + 1 + 6 + 120 + 10},
		{"tall answer is capped", 0, false, true, 900, quickPanelMaxHeight},
		{"fractional inset rounds up", 37.5, false, false, 0, 38 + 10 + 36 + 10},
	} {
		if got := quickPanelFitHeight(test.inset, test.progress, test.hairline, test.body); got != test.want {
			t.Errorf("%s: height = %d, want %d", test.name, got, test.want)
		}
	}
	if quickPanelMaxHeight != 340 || quickPanelWidth != 560 {
		t.Fatalf("panel cap/width = %d/%d, want 340/560", quickPanelMaxHeight, quickPanelWidth)
	}
}

func TestPendingAskIsNotAnExpandedAnswer(t *testing.T) {
	d := &desktop{quickAskPending: true, askLoading: true}
	if d.quickPanelExpanded() {
		t.Fatal("a pending ask must not count as an answer")
	}
	if !d.quickPanelProgressing() || !d.quickPanelBusy() {
		t.Fatal("pending ask should show progress and replace the recap")
	}
	if got := quickPanelFitHeight(0, true, true, 16); got > 100 {
		t.Fatalf("pending height = %d, want compact", got)
	}
	d = &desktop{quickConversation: []quickConversationTurn{{question: "q", answer: "a"}}}
	if !d.quickPanelExpanded() {
		t.Fatal("an answer should expand the panel")
	}
}

func TestQuickPanelEscapeAction(t *testing.T) {
	for _, test := range []struct {
		name    string
		content bool
		asking  bool
		since   time.Duration
		want    quickEscape
	}{
		{"first press with content clears", true, false, 0, quickEscapeClear},
		{"first press while asking clears", false, true, 0, quickEscapeClear},
		{"empty panel closes", false, false, 0, quickEscapeClose},
		{"second press within 600ms closes", true, false, 300 * time.Millisecond, quickEscapeClose},
		{"second press while asking within 600ms closes", true, true, 599 * time.Millisecond, quickEscapeClose},
		{"slow second press clears again", true, false, 700 * time.Millisecond, quickEscapeClear},
		{"empty panel after a slow press closes", false, false, 2 * time.Second, quickEscapeClose},
	} {
		if got := quickPanelEscapeAction(test.content, test.asking, test.since); got != test.want {
			t.Errorf("%s: got %v want %v", test.name, got, test.want)
		}
	}
}

func TestQuickPanelPointerExitKeepsContent(t *testing.T) {
	d := &desktop{}
	if d.quickPanelHasContent() {
		t.Fatal("empty panel reported content")
	}
	d.quickInput = "half-typed"
	if !d.quickPanelHasContent() {
		t.Fatal("typed text is content")
	}
	d = &desktop{quickConversation: []quickConversationTurn{{question: "q", answer: "a"}}}
	if !d.quickPanelHasContent() {
		t.Fatal("an answer is content")
	}
}

func TestQuickTurnMeta(t *testing.T) {
	for _, test := range []struct {
		turn quickConversationTurn
		want string
	}{
		{quickConversationTurn{sources: 3}, "Answered from 3 items sent to the local model"},
		{quickConversationTurn{sources: 1}, "Answered from 1 item sent to the local model"},
		{quickConversationTurn{direct: true}, "Answered without a model"},
		{quickConversationTurn{general: true}, "General answer, no memory used"},
		{quickConversationTurn{}, ""},
	} {
		if got := quickTurnMeta(test.turn); got != test.want {
			t.Errorf("quickTurnMeta(%+v) = %q, want %q", test.turn, got, test.want)
		}
	}
}

func TestQuickPanelModeLabelFollowsDetection(t *testing.T) {
	if quickPanelModeLabel("What did I read?") != "Ask" || quickPanelModeLabel("Call Mira") != "Note" {
		t.Fatal("mode label should follow quickPanelWantsAnswer")
	}
}

func TestQuickPanelSurfaceIsGraphite(t *testing.T) {
	if got := interactBackdropColor(); got != (color.NRGBA{R: 0x1B, G: 0x1D, B: 0x22, A: 0xff}) {
		t.Fatalf("surface = %#v, want #1B1D22", got)
	}
}

func TestQuickNoteFailureReopensOnlyForQuickNotes(t *testing.T) {
	if !quickNoteShouldReopenAfterFailure(true, true) || quickNoteShouldReopenAfterFailure(true, false) || quickNoteShouldReopenAfterFailure(false, true) {
		t.Fatal("reopen rules changed")
	}
}

func TestSavedNoteConfirmationSurvivesEmptyInput(t *testing.T) {
	d := &desktop{quickNotePending: true, quickInput: "Call Mira"}
	d.quickPanelNoteSaved()
	if d.quickInput != "" || !d.quickUI.savedFlash || !d.quickPanelHasContent() {
		t.Fatalf("saved state = input %q flash %v", d.quickInput, d.quickUI.savedFlash)
	}
}

func TestQuickPanelRevealStepWaitsForMeasurementThenGivesUp(t *testing.T) {
	if !quickPanelRevealStep(false, false, 0) {
		t.Fatal("a panel with no body needs no measurement")
	}
	if quickPanelRevealStep(true, false, 0) {
		t.Fatal("must wait for the first measurement")
	}
	if !quickPanelRevealStep(true, true, 0) {
		t.Fatal("measured content should be revealed")
	}
	if !quickPanelRevealStep(true, false, quickPanelRevealAttempts) {
		t.Fatal("must stop waiting eventually")
	}
}

func TestQuickEnterSavesANoteOnceFromAnyBinding(t *testing.T) {
	saves := 0
	d := &desktop{quickInput: "Call Mira about the launch"}
	d.quickUI.saveNoteFn = func() { saves++ }
	// Plain Return reported by the field and by the window shortcut at once.
	d.quickEnterPlain()
	d.quickEnter(false)
	if saves != 1 {
		t.Fatalf("plain Return saved %d times, want 1", saves)
	}
	// Command-Return after the debounce window, reported twice.
	d.quickUI.lastEnter = time.Now().Add(-time.Second)
	d.quickEnterWithCommand()
	d.quickEnter(true)
	if saves != 2 {
		t.Fatalf("Command-Return saved %d times in total, want 2", saves)
	}
}

func TestQuickEnterIgnoresEmptyDraftAndForcesNoteForQuestions(t *testing.T) {
	saves := 0
	d := &desktop{}
	d.quickUI.saveNoteFn = func() { saves++ }
	d.quickEnter(false)
	if saves != 0 {
		t.Fatal("empty draft must not save")
	}
	d.quickInput = "What did I read this morning?"
	d.quickEnter(true)
	d.quickUI.lastEnter = time.Time{}
	d.quickInput = "Recap my day"
	d.quickEnter(true)
	if saves != 2 {
		t.Fatalf("Command-Return on a question saved %d times, want 2 (forced note)", saves)
	}
}

func TestSaveQuickPanelNoteReportsUnavailableStoreInsteadOfSilence(t *testing.T) {
	d := &desktop{quickInput: "Call Mira"}
	d.saveQuickPanelNote()
	if d.status == "" || d.quickNotePending || d.quickInput != "Call Mira" {
		t.Fatalf("status=%q pending=%v input=%q; want a status line and the draft kept", d.status, d.quickNotePending, d.quickInput)
	}
}
