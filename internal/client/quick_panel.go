package client

import (
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/today"
)

func quickPanelShouldDismissAfterFocusChange(visible, focused bool) bool {
	return visible && !focused
}

func quickPanelPointerTransition(visible, armed, inside bool) (nextArmed, dismiss bool) {
	if !visible {
		return false, false
	}
	if inside {
		return true, false
	}
	return false, armed
}

func connectQuickPanelFocusDismissal(connect func(func(bool)), visible func() bool, dismiss func()) bool {
	if connect == nil || visible == nil || dismiss == nil {
		return false
	}
	connect(func(focused bool) {
		if quickPanelShouldDismissAfterFocusChange(visible(), focused) {
			dismiss()
		}
	})
	return true
}

type quickConversationTurn struct {
	question string
	answer   string
	sources  int
	general  bool
	direct   bool
	sent     int
	plan     assistant.Plan
}

// quickHistory converts answered turns into the follow-up context.
func quickHistoryTurns(turns []quickConversationTurn) []assistant.Turn {
	history := make([]assistant.Turn, 0, len(turns))
	for _, turn := range turns {
		history = append(history, assistant.Turn{Question: turn.question, Answer: turn.answer, Plan: turn.plan})
	}
	return history
}

func appendQuickConversation(turns []quickConversationTurn, question string, result assistant.Result) []quickConversationTurn {
	answer := result.Answer.Text
	if result.Answer.InsufficientEvidence {
		answer = "I couldn’t find enough in your saved memory to answer that."
	}
	if len(answer) > quickConversationBytes {
		answer = answer[:quickConversationBytes]
		for !utf8.ValidString(answer) {
			answer = answer[:len(answer)-1]
		}
		answer += "…"
	}
	plan := result.Plan
	if plan.Question == "" {
		plan, _ = assistant.PlanQuestion(question, time.Now(), nil)
	}
	turns = append(turns, quickConversationTurn{question: question, answer: answer, sources: len(result.Sources), general: result.General, direct: result.Direct, sent: result.SentItems, plan: plan})
	if len(turns) > quickConversationLimit {
		turns = append([]quickConversationTurn(nil), turns[len(turns)-quickConversationLimit:]...)
	}
	return turns
}

func quickPanelShowsDraftStatus(input, status string) bool {
	return strings.TrimSpace(input) != "" && strings.TrimSpace(status) != ""
}

func quickPanelShowsDayPreview(preview, pinned, expanded, hasDraftStatus bool) bool {
	return preview && !pinned && !expanded && !hasDraftStatus
}

func quickNoteShouldReopenAfterFailure(isQuickNote, failed bool) bool { return isQuickNote && failed }

type quickPanelFocusWindow interface {
	SetFocusedWidget(gui.Widget) bool
}

type quickPanelBackgroundWindow interface {
	SetStyleName(string)
}

func setQuickPanelHostBackground(window quickPanelBackgroundWindow) {
	if window != nil {
		// Styling the panel's content alone leaves the native window's exposed
		// client area in the app theme's gray. Set the host clear/fill too.
		window.SetStyleName("interact-background")
	}
}

func focusQuickPanelComposer(window quickPanelFocusWindow, input gui.Widget) bool {
	return window != nil && input != nil && window.SetFocusedWidget(input)
}

func quickPanelShouldFocusInput(hovered, pinned bool) bool {
	return hovered || pinned
}

func focusQuickPanelInput(hovered, pinned bool, window quickPanelFocusWindow, input gui.Widget) bool {
	return quickPanelShouldFocusInput(hovered, pinned) && focusQuickPanelComposer(window, input)
}

var quickAskPrefixes = []string{"what ", "who ", "when ", "where ", "why ", "how ", "which ", "can ", "could ", "did ", "do ", "does ", "is ", "are ", "was ", "were ", "have ", "has ", "should ", "would ",
	"give me", "show me", "tell me", "summarize", "summarise", "recap", "list", "find", "search", "remind me what", "explain", "describe", "pull up", "look up", "get me", "brief me", "catch me up",
	"any ", "anything ", "can you", "could you", "what's", "whats", "who's", "where's", "when's", "how's"}

func quickPanelWantsAnswer(input string) bool {
	query := strings.TrimSpace(input)
	if query == "" {
		return false
	}
	query = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(query), "ask:"))
	query = strings.TrimSpace(strings.TrimPrefix(query, "please "))
	if strings.HasSuffix(query, "?") {
		return true
	}
	for _, prefix := range quickAskPrefixes {
		if !strings.HasPrefix(query, prefix) {
			continue
		}
		rest := query[len(prefix):]
		if strings.HasSuffix(prefix, " ") || rest == "" {
			return true
		}
		if r, _ := utf8.DecodeRuneInString(rest); !unicode.IsLetter(r) && r != '\'' {
			return true
		}
	}
	for _, word := range strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) }) {
		if word == "recap" || word == "summary" {
			return true
		}
	}
	return false
}

func quickPanelWindowIDs(dashboardOpen, quickMounted bool) []string {
	ids := make([]string, 0, 2)
	if quickMounted {
		ids = append(ids, "quick")
	}
	if dashboardOpen {
		ids = append(ids, "main")
	}
	return ids
}

func (d *desktop) quickWindowView() ui.WindowView {
	title, content := "Blur", d.quickPanel()
	window := ui.Window("quick").Title(title).
		Size(quickPanelWidth, float32(quickPanelCreationHeight())).MinSize(quickPanelWidth, float32(quickPanelCreationHeight())).Center(true).
		Chrome(ui.WindowChromeNone).Transparent(false).Content(content)
	window = addDesktopBackgroundClose(window, d.app, "quick", func() {
		d.quickPanelPinned = false
		d.quickPreview = false
		d.quickHoverEpoch++
		d.quickPanelPointerArmed = false
		d.collapseTransientContent("quick")
	})
	return addDesktopQuickShortcut(window, d.app, d.handleQuickPanelEscape,
		ui.Shortcut(ui.KeyEnter).Modifiers(ui.ModCommand).Enabled(strings.TrimSpace(d.quickInput) != "").OnActivate(d.quickEnterWithCommand),
		ui.Shortcut(ui.KeyC).Modifiers(ui.ModCommand).Enabled(d.quickCopyAvailable()).OnActivate(d.copyLatestQuickAnswer),
		ui.Shortcut(ui.KeyArrowUp).Enabled(len(d.quickHistory.entries) > 0).OnActivate(func() {
			if value, ok := d.quickHistory.Previous(d.quickInput); ok {
				d.setQuickHistoryInput(value)
			}
		}),
		ui.Shortcut(ui.KeyArrowDown).Enabled(d.quickHistory.active).OnActivate(func() {
			if value, ok := d.quickHistory.Next(); ok {
				d.setQuickHistoryInput(value)
			}
		}),
		ui.Shortcut(ui.KeyTab).Enabled(d.quickHistory.CanComplete(d.quickInput)).OnActivate(func() {
			if value, ok := d.quickHistory.Complete(d.quickInput); ok {
				d.setQuickHistoryInput(value)
			}
		}),
		ui.Shortcut(ui.KeyEnter).Enabled(strings.TrimSpace(d.quickInput) != "").OnActivate(d.quickEnterPlain),
	)
}

func (d *desktop) watchQuickPanelFocus() {
	if d == nil || d.app == nil || d.quickFocusWatching {
		return
	}
	d.quickFocusWatching = true
	d.app.Post(func() {
		window := d.app.FindWindow("quick")
		if window == nil {
			d.quickFocusWatching = false
			return
		}
		connectQuickPanelFocusDismissal(
			func(handler func(bool)) { window.ConnectFocus(handler) },
			func() bool { return d.quickPreview || d.quickPanelPinned },
			d.dismissQuickPanel,
		)
	})
}

func (d *desktop) setQuickHistoryInput(value string) {
	if d == nil {
		return
	}
	d.quickInput = value
	d.status = ""
	if d.app != nil {
		d.app.RequestUpdate()
	}
}

type quickPreviewGroup struct {
	title string
	items []string
}

func quickPreviewGroups(snapshot today.Snapshot, summaries []chronicle.Summary) []quickPreviewGroup {
	groups := []quickPreviewGroup{
		{title: "Highlights", items: previewChangeTexts(snapshot.RecentChanges)},
		{title: "To do", items: previewTodoTexts(snapshot.ConfirmedTodos)},
		{title: "Possible", items: previewTodoTexts(snapshot.TodoCandidates)},
		{title: "Open", items: previewLoopTexts(snapshot.OpenLoops)},
		{title: "Follow-up", items: previewQuestionTexts(snapshot.Questions)},
		{title: "Recap", items: quickPreviewRecaps(summaries)},
	}
	for i := range groups {
		if len(groups[i].items) > 2 {
			groups[i].items = groups[i].items[:2]
		}
		maxText := 88
		if groups[i].title == "Recap" {
			maxText = 104
		}
		for j := range groups[i].items {
			groups[i].items[j] = compactPreviewText(groups[i].items[j], maxText)
		}
	}
	return groups
}

func previewChangeTexts(items []today.Change) []string {
	texts := make([]string, 0, min(2, len(items)))
	for _, item := range items {
		text := strings.TrimSpace(item.Text)
		if item.Origin == today.ChangeOriginUser {
			continue
		}
		// The deterministic activity fallback is intentionally not presented as
		// a recap; app names alone are not useful day-level information.
		if strings.HasPrefix(strings.ToLower(text), "active in ") || len(strings.Fields(text)) < 2 {
			continue
		}
		texts = append(texts, text)
		if len(texts) == 2 {
			break
		}
	}
	return texts
}

func previewTodoTexts(items []today.Todo) []string {
	texts := make([]string, 0, len(items))
	for _, item := range items {
		if text := strings.TrimSpace(item.Text); text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

func previewLoopTexts(items []today.OpenLoop) []string {
	texts := make([]string, 0, len(items))
	for _, item := range items {
		if text := strings.TrimSpace(item.Text); text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

func previewQuestionTexts(items []today.Question) []string {
	texts := make([]string, 0, len(items))
	for _, item := range items {
		if !item.Resolved {
			if text := strings.TrimSpace(item.Text); text != "" {
				texts = append(texts, text)
			}
		}
	}
	return texts
}

func quickPreviewHasUsefulContent(snapshot today.Snapshot, summaries []chronicle.Summary) bool {
	return len(previewChangeTexts(snapshot.RecentChanges))+len(snapshot.ConfirmedTodos)+len(snapshot.TodoCandidates)+len(snapshot.OpenLoops)+len(previewQuestionTexts(snapshot.Questions))+len(quickPreviewRecaps(summaries)) > 0
}

func quickPreviewRecaps(items []chronicle.Summary) []string {
	if len(items) == 0 {
		return nil
	}
	ordered := append([]chronicle.Summary(nil), items...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Start.After(ordered[j].Start) })
	result := make([]string, 0, min(2, len(ordered)))
	for _, item := range ordered {
		text := firstRecapSentence(item.Text)
		if text == "" {
			continue
		}
		result = append(result, compactPreviewText(text, 104))
		if len(result) == 2 {
			break
		}
	}
	return result
}

func firstRecapSentence(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	for index, r := range value {
		if r == '.' || r == '?' || r == '!' {
			return strings.TrimSpace(value[:index+len(string(r))])
		}
	}
	return value
}

func compactPreviewText(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= max {
		return value
	}
	value = value[:max-3]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "…"
}

func (d *desktop) askFromQuickPanel() {
	if d == nil || d.client == nil || !d.client.CanAskMemory() || d.askLoading {
		return
	}
	d.askQuestion = strings.TrimSpace(d.quickInput)
	d.quickHistory.Record(d.askQuestion)
	d.askMemory()
}

// quickCopyAvailable reports whether Command-C copies the latest answer: only
// with an empty composer, so the text field keeps its own copy.
func (d *desktop) quickCopyAvailable() bool {
	return d != nil && strings.TrimSpace(d.quickInput) == "" && len(d.quickConversation) > 0
}

func (d *desktop) copyLatestQuickAnswer() {
	if !d.quickCopyAvailable() {
		return
	}
	d.copyQuickAnswer(d.quickConversation[len(d.quickConversation)-1].answer)
}

// carryQuickToAsk moves the latest quick exchange into the Ask page state.
func (d *desktop) carryQuickToAsk() {
	n := len(d.quickConversation)
	if n == 0 {
		return
	}
	turn := d.quickConversation[n-1]
	result := &assistant.Result{Answer: assistant.Answer{Text: turn.answer}, General: turn.general}
	if d.askResult != nil {
		kept := *d.askResult
		result = &kept
	}
	d.page, d.status = "Ask", ""
	d.askQuestion, d.askResult = turn.question, result
	d.expandedSourceID, d.expandedAnswer = "", false
}

// openDashboard continues the latest quick exchange on the Ask page.
func (d *desktop) openDashboard() {
	if d == nil || d.app == nil || d.setupRequired {
		return
	}
	// Hiding the panel invalidates the ask state, so carry the exchange after.
	result := d.askResult
	if quick := d.app.FindWindow("quick"); quick != nil {
		d.collapseTransientContent("quick")
		_ = quick.PlatformWindow().Hide()
	}
	d.askResult = result
	d.carryQuickToAsk()
	d.dashboardOpen = true
	d.app.RequestUpdate()
}

// collapseTransientContent clears expanded answers/source text when a window
// is dismissed, while preserving unsaved note and search drafts.
func (d *desktop) collapseTransientContent(windowID string) {
	if d == nil {
		return
	}
	switch windowID {
	case "quick":
		d.invalidateAsk()
		d.resizeQuickPanel(false)
	case "main":
		d.invalidateAsk()
		d.clearKnowledgeSuggestions()
		d.expandedSourceID = ""
		d.expandedChronicleID = ""
		d.chronicleSources = nil
		d.chronicleSourcesNext = ""
		d.chronicleSourcesLoading = false
		d.quickNoteExpanded = false
	}
	if d.app != nil {
		d.app.RequestUpdate()
	}
}

func (d *desktop) openQuickPanel() {
	if d == nil || d.app == nil {
		return
	}
	d.app.Post(func() {
		d.quickHoverEpoch++
		d.quickPreview = false
		d.quickPanelPinned = true
		d.quickPanelMounted = true
		d.app.RequestUpdate()
		d.app.Post(d.presentQuickPanel)
	})
}

type quickPanelPresentWindow interface {
	Show() error
	SetStyleName(string)
	SetFocusedWidget(gui.Widget) bool
}

func (d *desktop) presentQuickPanel() {
	if d == nil || d.app == nil || !d.quickPanelMounted {
		return
	}
	quick := d.app.FindWindow("quick")
	if quick == nil {
		// Mounting a requested native window is asynchronous; retry after the
		// declarative update has had a turn to complete.
		d.app.TimeoutFunc(25*time.Millisecond, func() {
			if window := d.app.FindWindow("quick"); window != nil {
				d.showQuickPanelWindow(window)
			}
		})
		return
	}
	d.showQuickPanelWindow(quick)
}

func (d *desktop) focusQuickPanelComposerWhenReady(attempt int) {
	if d == nil || d.app == nil || !quickPanelShouldFocusInput(d.quickPreview, d.quickPanelPinned) {
		return
	}
	quick := d.app.FindWindow("quick")
	if quick != nil && focusQuickPanelInput(d.quickPreview, d.quickPanelPinned, quick, d.app.FindWidget("quick", "quick-input")) {
		return
	}
	if attempt >= 3 {
		slog.Debug("quick capture input focus unavailable", "result", "not_focused")
		return
	}
	d.app.TimeoutFunc(25*time.Millisecond, func() {
		d.focusQuickPanelComposerWhenReady(attempt + 1)
	})
}

// watchQuickPanelPointer attaches to GoUI's native pointer-crossing events.
// This stays idle while the pointer is stationary and is scoped to Interact.
func (d *desktop) watchQuickPanelPointer() {
	if d == nil || d.app == nil || (!d.quickPreview && !d.quickPanelPinned) {
		return
	}
	widget := d.app.FindWidget("quick", "quick-panel-root")
	if widget == nil || widget == d.quickPanelPointerWidget && d.quickPanelPointerController != nil {
		return
	}
	if d.quickPanelPointerWidget != nil && d.quickPanelPointerController != nil {
		d.quickPanelPointerWidget.RemoveEventController(d.quickPanelPointerController)
	}
	controller := gui.NewMotionEventController()
	controller.ConnectContainsHover(d.onQuickPanelPointer)
	widget.AddEventController(controller)
	d.quickPanelPointerWidget, d.quickPanelPointerController = widget, controller
	d.quickPanelPointerArmed = desktopQuickPanelPointerInside(d.app)
}
