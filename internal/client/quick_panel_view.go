package client

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/platform/graphics"
	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/assistant"
)

const (
	quickPanelEnterDebounce = 150 * time.Millisecond
	quickPanelDoublePress   = 600 * time.Millisecond
	quickPanelSavedHold     = 1200 * time.Millisecond
	quickPanelNotchDelay    = 150 * time.Millisecond
	quickPanelProgressSeg   = 120
	quickPanelQuestionRune  = 72
)

// quickEscape is what one Escape press does.
type quickEscape int

const (
	quickEscapeClear quickEscape = iota // cancel any ask, empty the panel
	quickEscapeClose                    // close the panel
)

// quickPanelEscapeAction decides one Escape press. The first press clears the
// panel (cancelling an ask in flight); a second press within 600 ms, or any
// press on an already empty panel, closes it. since is the time since the
// previous press; a zero or negative value means there was none.
func quickPanelEscapeAction(hasContent, asking bool, since time.Duration) quickEscape {
	if since > 0 && since < quickPanelDoublePress {
		return quickEscapeClose
	}
	if hasContent || asking {
		return quickEscapeClear
	}
	return quickEscapeClose
}

func (d *desktop) handleQuickPanelEscape() {
	if d == nil {
		return
	}
	x := d.quickExtras()
	now := time.Now()
	since := time.Duration(0)
	if !x.lastEscape.IsZero() {
		since = now.Sub(x.lastEscape)
	}
	if quickPanelEscapeAction(d.quickPanelHasContent(), d.askLoading, since) == quickEscapeClose {
		x.lastEscape = time.Time{}
		d.dismissQuickPanel()
		return
	}
	x.lastEscape = now
	d.clearQuickPanel()
}

// clearQuickPanel returns the panel to its empty compact state.
func (d *desktop) clearQuickPanel() {
	if d.askLoading {
		d.cancelAsk()
	}
	d.invalidateAsk()
	d.quickInput, d.status = "", ""
	d.quickConversation, d.quickAskPending = nil, false
	d.quickUI.savedFlash, d.quickUI.expandedTurns = false, nil
	d.quickHistory.resetBrowsing()
	d.resizeQuickPanel(false)
	if d.app != nil {
		d.app.RequestUpdate()
	}
}

// quickHairline is a 1 px divider in the panel's own (always dark) palette.
func quickHairline() ui.View {
	return ui.VBox().Style(styleQuickHairline).MinHeight(1).MaxHeight(1)
}

func spacer(height float32) ui.View {
	return ui.VBox().MinHeight(height).MaxHeight(height)
}

// quickPanel builds the panel: composer on top (below the camera housing), an
// optional progress line, then a transcript or the day recap. Plain text on
// the graphite surface; no bubbles.
func (d *desktop) quickPanel() ui.View {
	input := ui.TextInput().ID("quick-input").Text(d.quickInput).Placeholder("Ask or add a note").
		Style(styleQuickInput).
		OnText(func(value string) {
			hadStatus := quickPanelShowsDraftStatus(d.quickInput, d.status) || d.quickUI.savedFlash
			d.quickInput = value
			d.quickHistory.resetBrowsing()
			d.expandedAnswer = false
			d.status = ""
			d.quickUI.savedFlash = false
			if hadStatus {
				d.resizeQuickPanel(false)
			}
			d.app.RequestUpdate()
		}).OnSubmit(d.quickEnterFromField).MainWeight(1)

	composer := []ui.View{
		input,
	}
	if trimmed(d.quickInput) != "" {
		composer = append(composer, ui.Label(quickPanelModeLabel(d.quickInput)).Style(styleQuickAccent))
	}
	rows := []ui.View{
		ui.HBox(composer...).Spacing(10).CrossAlign(layout.CrossCenter).MinHeight(quickPanelComposerHeight).MaxHeight(quickPanelComposerHeight),
	}
	if d.quickPanelProgressing() {
		rows = append(rows, d.quickProgressBar())
	}
	if body := d.quickPanelBody(); body != nil {
		if d.quickPanelHairline() {
			rows = append(rows, quickHairline())
		}
		rows = append(rows, pageScroll(body).MainWeight(1))
	}
	return ui.VBox(rows...).Paddings(quickPanelInsets()).Spacing(quickPanelBodyGap).CrossAlign(layout.CrossStretch).
		Style(styleQuickBackground).ID("quick-panel-root").MinWidth(quickPanelWidth)
}

func quickPanelModeLabel(input string) string {
	if quickPanelWantsAnswer(input) {
		return "Ask"
	}
	return "Note"
}

// quickProgressBar is the 2 px indeterminate amber line under the composer:
// a 120 px segment sweeping left to right, or a static line under Reduce Motion.
func (d *desktop) quickProgressBar() ui.View {
	q := quickPalette()
	x := &d.quickUI
	static := x.reduceMotion || desktopReduceMotion()
	return ui.DrawingArea(func(p gui.Painter, size geometry.Size) {
		brush := graphics.ColorOf(q.amber)
		if static {
			p.FillRect(geometry.Rect(0, 0, size.Width, size.Height), brush)
		} else {
			left := x.progressPhase*(size.Width+quickPanelProgressSeg) - quickPanelProgressSeg
			right := left + quickPanelProgressSeg
			left, right = max(left, 0), min(right, size.Width)
			if right > left {
				p.FillRect(geometry.Rect(left, 0, right-left, size.Height), brush)
			}
		}
		x.progressDrawn = x.progressPhase
	}).OnShouldDraw(func(need *bool) {
		if !static && x.progressDrawn != x.progressPhase {
			*need = true
		}
	}).ID("quick-progress").MinHeight(quickPanelProgressHeight).MaxHeight(quickPanelProgressHeight)
}

// quickPanelBody is the transcript, status line or day recap; nil when the
// panel is only a composer.
func (d *desktop) quickPanelBody() ui.View {
	var rows []ui.View
	switch {
	case d.quickPanelShowsPreview():
		rows = d.quickPreviewRows()
	default:
		rows = d.quickTranscriptRows()
	}
	if len(rows) == 0 {
		return nil
	}
	return ui.VBox(rows...).CrossAlign(layout.CrossStretch).ID("quick-body")
}

func (d *desktop) quickPreviewRows() []ui.View {
	if d.todayLoading {
		return []ui.View{wrapLabel("Updating your local recap…").Style(styleQuickMeta)}
	}
	var rows []ui.View
	remaining := quickPanelPreviewRows
	for _, group := range quickPreviewGroups(d.today.Snapshot, d.hourlySummaries) {
		if len(group.items) == 0 || remaining == 0 {
			continue
		}
		if len(rows) > 0 {
			rows = append(rows, spacer(6))
		}
		rows = append(rows, wrapLabel(group.title).Style(styleQuickGroup), spacer(2))
		for _, item := range group.items[:min(len(group.items), remaining)] {
			rows = append(rows, wrapLabel(item).Style(styleQuickRow))
			remaining--
		}
	}
	return rows
}

func (d *desktop) quickTranscriptRows() []ui.View {
	var rows []ui.View
	last := len(d.quickConversation) - 1
	for i, turn := range d.quickConversation {
		if i > 0 {
			rows = append(rows, spacer(10), quickHairline(), spacer(10))
		}
		open := i == last || d.quickUI.expandedTurns[i]
		rows = append(rows, d.quickQuestionLine(i, turn, open, i != last))
		if !open {
			continue
		}
		rows = append(rows, spacer(8), wrapLabel(turn.answer).Style(styleQuickAnswer))
		if meta := quickTurnMeta(turn); meta != "" {
			rows = append(rows, spacer(10), wrapLabel(meta).Style(styleQuickMeta))
		}
		if i == last {
			rows = append(rows, spacer(12), quickHairline(), d.quickActionRow(turn))
		}
	}
	switch {
	case d.quickAskPending && d.askLoading:
		if len(rows) > 0 {
			rows = append(rows, spacer(10))
		}
		rows = append(rows, wrapLabel(quickPanelPendingLabel(d.askQuestion)).Style(styleQuickMeta))
	case d.quickUI.savedFlash:
		rows = append(rows, wrapLabel("Saved to Today").Style(styleQuickAccent))
	case d.quickUI.copied:
		rows = append(rows, spacer(8), wrapLabel("Answer copied").Style(styleQuickAccent))
	case quickPanelShowsDraftStatus(d.quickInput, d.status):
		if len(rows) > 0 {
			rows = append(rows, spacer(10))
		}
		rows = append(rows, wrapLabel(d.status).Style(styleQuickMeta))
	}
	return rows
}

// answerSentLine is the one-line statement of what went to a model.
func answerSentLine(general, direct bool, sent, sources int) string {
	switch {
	case general:
		return "General answer, no memory used"
	case direct:
		return "Answered without a model"
	}
	items := sent
	if items == 0 {
		items = sources
	}
	switch {
	case items == 1:
		return "Answered from 1 item sent to the local model"
	case items > 1:
		return fmt.Sprintf("Answered from %d items sent to the local model", items)
	}
	return ""
}

func quickTurnMeta(turn quickConversationTurn) string {
	return answerSentLine(turn.general, turn.direct, turn.sent, turn.sources)
}

// quickQuestionLine is the muted one-line question above an answer. Earlier
// turns are collapsed to this line; clicking one expands it.
func (d *desktop) quickQuestionLine(index int, turn quickConversationTurn, open, clickable bool) ui.View {
	label := ui.Label(compactPreviewText(turn.question, quickPanelQuestionRune)).Style(styleQuickQuestion)
	if !clickable {
		return label
	}
	return ui.Button().Content(label).Style(modern.Flat).
		Paddings(geometry.Insets{}).OnClick(func() {
		if d.quickUI.expandedTurns == nil {
			d.quickUI.expandedTurns = map[int]bool{}
		}
		d.quickUI.expandedTurns[index] = !open
		d.resizeQuickPanel(false)
		d.app.RequestUpdate()
	})
}

func (d *desktop) quickActionRow(turn quickConversationTurn) ui.View {
	open := flatButton("Open in Blur").MinHeight(28).OnClick(d.openDashboard)
	return ui.HBox(open).Spacing(4).MainAlign(layout.MainStart)
}

func (d *desktop) copyQuickAnswer(text string) {
	if err := copyToPasteboard(text); err != nil {
		slog.Warn("quick ask copy failed", "result", "failed")
		return
	}
	x := &d.quickUI
	x.copied = true
	x.copyEpoch++
	epoch := x.copyEpoch
	d.app.RequestUpdate()
	d.app.TimeoutFunc(quickPanelSavedHold, func() {
		if epoch == x.copyEpoch {
			x.copied = false
			d.app.RequestUpdate()
		}
	})
}

// quickEnter is the single entry for Return and Command-Return, wired to the
// text field's submit and to both window shortcuts. The text field reports
// Command-Return as a plain submit, so the caller passes whether Command is
// held. A press is handled once even if more than one binding reports it.
func (d *desktop) quickEnter(forceNote bool) {
	if d == nil || trimmed(d.quickInput) == "" {
		return
	}
	now := time.Now()
	if !d.quickUI.lastEnter.IsZero() && now.Sub(d.quickUI.lastEnter) < quickPanelEnterDebounce {
		return
	}
	d.quickUI.lastEnter = now
	if forceNote {
		slog.Info("Interact input submitted", "intent", "note", "input_bytes", len(d.quickInput), "forced", true)
		d.quickSaveNote()
		return
	}
	d.submitQuickPanelInput()
}

// quickSaveNote saves the draft as a note; tests replace it through saveNoteFn.
func (d *desktop) quickSaveNote() {
	if d.quickUI.saveNoteFn != nil {
		d.quickUI.saveNoteFn()
		return
	}
	d.saveQuickPanelNote()
}

func (d *desktop) quickEnterFromField()   { d.quickEnter(commandKeyHeld()) }
func (d *desktop) quickEnterPlain()       { d.quickEnter(false) }
func (d *desktop) quickEnterWithCommand() { d.quickEnter(true) }

func (d *desktop) submitQuickPanelInput() {
	if d == nil || trimmed(d.quickInput) == "" {
		return
	}
	if quickPanelWantsAnswer(d.quickInput) {
		slog.Info("Interact input submitted", "intent", "ask", "input_bytes", len(d.quickInput))
		if d.loading || d.client == nil || !d.client.CanAskMemory() || d.askLoading || d.client.MemoryIsChanging() {
			d.status = "Ask is unavailable. Check AI setup or wait for the current operation. Your text is still here."
			d.resizeQuickPanel(false)
			d.app.RequestUpdate()
			return
		}
		d.quickAskPending = true
		d.askFromQuickPanel()
		if d.askLoading {
			d.quickInput = ""
			d.resizeQuickPanel(false)
			d.app.RequestUpdate()
		} else {
			d.quickAskPending = false
		}
		return
	}
	slog.Info("Interact input submitted", "intent", "note", "input_bytes", len(d.quickInput))
	d.quickSaveNote()
}

// saveQuickPanelNote saves the draft as a note regardless of how it reads. It
// needs only the open store, never a running capture. Anything that stops the
// save is shown in the panel's status line rather than swallowed.
func (d *desktop) saveQuickPanelNote() {
	if d == nil || trimmed(d.quickInput) == "" {
		return
	}
	switch {
	case d.client == nil || !d.client.CanAddNote():
		d.status = "Notes are unavailable until encrypted storage is set up. Your text is still here."
	case d.loading:
		d.status = "Blur is busy. Press Return again in a moment. Your text is still here."
	default:
		d.note = trimmed(d.quickInput)
		d.quickNotePending = true
		d.addNote()
		if !d.loading { // addNote declined without starting a save
			d.quickNotePending = false
			d.status = "The note could not be saved. Your text is still here."
		}
	}
	d.resizeQuickPanel(false)
	if d.app != nil {
		d.app.RequestUpdate()
	}
}

// quickPanelNoteSaved runs when a quick note saved: the input is emptied and
// the panel confirms for a moment (see dismissQuickPanel).
func (d *desktop) quickPanelNoteSaved() {
	if !d.quickNotePending {
		return
	}
	d.quickNotePending = false
	d.quickInput = ""
	d.quickUI.savedFlash = true
}

func quickPanelPendingLabel(question string) string {
	if assistant.QuestionSeeksPersonalMemory(question) {
		return "Searching memories… Esc to stop"
	}
	return "Asking… Esc to stop"
}

// dismissQuickPanel hides the panel. After a saved note it first holds the
// "Saved to Today" confirmation for 1.2 s.
func (d *desktop) dismissQuickPanel() {
	if d == nil || d.app == nil || d.quickUI.snapshotRun {
		return
	}
	d.app.Post(func() {
		x := &d.quickUI
		if x.savedFlash && desktopQuickPanelVisible(d.app) {
			x.savedEpoch++
			epoch := x.savedEpoch
			d.quickPanelPinned, d.quickPreview, d.quickPanelPointerArmed = true, false, false
			d.resizeQuickPanel(false)
			d.app.RequestUpdate()
			d.app.TimeoutFunc(quickPanelSavedHold, func() {
				if epoch == x.savedEpoch && x.savedFlash {
					x.savedFlash = false
					d.hideQuickPanel()
				}
			})
			return
		}
		d.hideQuickPanel()
	})
}

func (d *desktop) hideQuickPanel() {
	if d.quickUI.snapshotRun {
		return
	}
	d.quickUI.revealEpoch++
	d.quickPanelPinned = false
	d.quickPreview = false
	d.quickHoverEpoch++
	d.quickPanelPointerArmed = false
	d.quickUI.savedFlash = false
	d.quickUI.savedEpoch++
	if quick := d.app.FindWindow("quick"); quick != nil {
		d.collapseTransientContent("quick")
		_ = quick.PlatformWindow().Hide()
	}
}

// toggleQuickPanel is the global-shortcut action: open focused, or close.
func (d *desktop) toggleQuickPanel() {
	if d == nil || d.app == nil {
		return
	}
	d.app.Post(func() {
		if d.quickPanelPinned && desktopQuickPanelVisible(d.app) {
			d.hideQuickPanel()
			return
		}
		activateDesktopApp()
		d.openQuickPanel()
	})
}

// quickPanelRevealStep decides, while the panel is mounted but still fully
// transparent, whether it can be revealed. It waits for the content to be laid
// out and measured (needsMeasure) and gives up after maxAttempts ticks so a
// missing measurement can never leave the panel invisible.
func quickPanelRevealStep(needsMeasure, measured bool, attempt int) (reveal bool) {
	if !needsMeasure || measured {
		return true
	}
	return attempt >= quickPanelRevealAttempts
}

const (
	quickPanelRevealTick     = 30 * time.Millisecond
	quickPanelRevealAttempts = 6
)

// showQuickPanelWindow presents the panel without ever showing an empty frame:
//
//  1. a hidden panel is made fully transparent before it is ordered front;
//  2. one update turn lays out the current content;
//  3. the content is measured and the final frame is set without animation;
//  4. only then does the panel fade in.
//
// A panel that is already visible just animates to its new content height.
func (d *desktop) showQuickPanelWindow(quick quickPanelPresentWindow) {
	setQuickPanelHostBackground(quick)
	visible := desktopQuickPanelVisible(d.app)
	snapshot := d.quickUI.snapshotRun
	if !visible && !snapshot {
		_ = setDesktopQuickPanelAlpha(d.app, 0)
	}
	// Show first, then reapply the native surface: AppKit may restore its
	// default window treatment while un-hiding a borderless window.
	if err := d.showQuickWindowForRun(quick); err != nil {
		slog.Warn("quick capture panel could not be shown", "result", "failed")
		return
	}
	if err := resizeDesktopQuickPanel(d.app, float64(d.quickPanelDesiredHeight())); err != nil {
		slog.Warn("quick capture placement unavailable", "result", "failed")
	}
	if visible || snapshot {
		d.resizeQuickPanel(false)
		d.finishShowingQuickPanel()
		return
	}
	d.quickUI.revealEpoch++
	d.revealQuickPanel(d.quickUI.revealEpoch, 0)
	d.app.RequestUpdate()
	d.finishShowingQuickPanel()
}

// revealQuickPanel waits for layout while the panel is transparent, sets the
// measured frame instantly, then fades in.
func (d *desktop) revealQuickPanel(epoch uint64, attempt int) {
	d.app.TimeoutFunc(quickPanelRevealTick, func() {
		if epoch != d.quickUI.revealEpoch {
			return
		}
		if !d.quickPreview && !d.quickPanelPinned {
			return // dismissed while hidden: stay transparent, nothing to show
		}
		_, measured := d.measureQuickPanelBody()
		if !quickPanelRevealStep(d.quickPanelBodyEstimate() > 0, measured, attempt) {
			d.revealQuickPanel(epoch, attempt+1)
			return
		}
		_ = resizeDesktopQuickPanel(d.app, float64(d.quickPanelDesiredHeight()))
		_ = fadeInDesktopQuickPanel(d.app)
	})
}

func (d *desktop) finishShowingQuickPanel() {
	d.watchQuickPanelFocus()
	d.watchQuickPanelPointer()
	if !d.quickUI.snapshotRun && quickPanelShouldFocusInput(d.quickPreview, d.quickPanelPinned) {
		d.focusQuickPanelComposerWhenReady(0)
	}
	if d.quickPanelPinned && d.setupRequired && !d.captureStarting {
		d.startPermissionWatch()
	}
}

// onNotchHover opens the day preview after a short delay so a pointer merely
// passing the notch does not open it.
func (d *desktop) onNotchHover(hovered bool) {
	if d == nil || d.app == nil {
		return
	}
	d.app.Post(func() {
		d.quickHoverEpoch++
		epoch := d.quickHoverEpoch
		if hovered {
			if d.quickPanelPinned {
				return
			}
			d.app.TimeoutFunc(quickPanelNotchDelay, func() {
				if epoch != d.quickHoverEpoch || d.quickPanelPinned {
					return
				}
				d.quickPreview = true
				d.quickPanelMounted = true
				d.app.RequestUpdate()
				d.app.Post(d.presentQuickPanel)
			})
			return
		}
		// Allow time to move from the notch to the preview before dismissing.
		d.app.TimeoutFunc(900*time.Millisecond, func() {
			if epoch != d.quickHoverEpoch || d.quickPanelPinned || !d.quickPreview {
				return
			}
			if desktopQuickPanelPointerInside(d.app) {
				return
			}
			d.quickPreview = false
			if quick := d.app.FindWindow("quick"); quick != nil {
				_ = quick.PlatformWindow().Hide()
			}
			d.app.RequestUpdate()
		})
	})
}

// onQuickPanelPointer dismisses on pointer exit only when there is nothing to
// lose: with typed text, an answer or a pending ask only Esc, click-outside or
// focus loss close the panel.
func (d *desktop) onQuickPanelPointer(inside bool) {
	if d == nil || d.app == nil {
		return
	}
	d.app.Post(func() {
		armed, dismiss := quickPanelPointerTransition(d.quickPreview || d.quickPanelPinned, d.quickPanelPointerArmed, inside)
		d.quickPanelPointerArmed = armed
		if inside {
			d.quickHoverEpoch++
		}
		if dismiss && !d.quickPanelHasContent() {
			d.dismissQuickPanel()
		}
	})
}

// showQuickWindowForRun shows the panel. During the UI snapshot run it only
// orders the window front: it neither activates the app nor takes key focus.
func (d *desktop) showQuickWindowForRun(quick quickPanelPresentWindow) error {
	if d.quickUI.snapshotRun {
		if w := d.app.FindWindow("quick"); w != nil {
			if err := orderWindowFrontQuietly(w.PlatformWindow().NativeHandle()); err == nil {
				return nil
			}
		}
	}
	return quick.Show()
}
