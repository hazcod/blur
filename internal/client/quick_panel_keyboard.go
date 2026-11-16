package client

import (
	"fmt"
	"strings"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/platform/events"
)

// Capture popup keys before the focused editor consumes arrows or Command-C.
// Modified arrows and copying a draft keep the editor's normal behavior.
func (d *desktop) newQuickPanelKeyController() *gui.KeyEventController {
	controller := gui.NewKeyEventController()
	controller.SetPhase(gui.PhaseCapture)
	controller.ConnectKeyDown(func(ctx gui.EventContext, event events.KeyEvent) {
		if d.handleQuickPanelKey(event) {
			ctx.StopPropagation()
			event.PreventDefault()
		}
	})
	return controller
}

func (d *desktop) watchQuickPanelKeyboard() {
	if d == nil || d.app == nil {
		return
	}
	widget := d.app.FindWidget("quick", "quick-panel-root")
	x := &d.quickUI
	if widget == nil || widget == x.keyboardWidget && x.keyboardController != nil {
		return
	}
	if x.keyboardWidget != nil && x.keyboardController != nil {
		x.keyboardWidget.RemoveEventController(x.keyboardController)
	}
	x.keyboardController = d.newQuickPanelKeyController()
	x.keyboardWidget = widget
	widget.AddEventController(x.keyboardController)
}

func (d *desktop) handleQuickPanelKey(event events.KeyEvent) bool {
	if d == nil {
		return false
	}
	if event.Key == events.KeyC && event.Modifiers == events.ModifierCommand && d.quickCopyAvailable() {
		d.copyLatestQuickAnswer()
		return true
	}
	if event.Key == events.KeyS && event.Modifiers == events.ModifierCommand && d.quickNoteSaveAvailable() {
		d.saveQuickSelectionAsNote()
		return true
	}
	if event.Modifiers != 0 {
		return false
	}
	switch event.Key {
	case events.KeyEscape:
		d.handleQuickPanelEscape()
		return true
	case events.KeyArrowUp:
		return d.navigateQuickPanel(-1)
	case events.KeyArrowDown:
		return d.navigateQuickPanel(1)
	}
	return false
}

func (d *desktop) navigateQuickPanel(direction int) bool {
	if len(d.quickConversation) > 0 && !d.quickHistory.active {
		if !d.quickUI.historyOpen {
			if direction < 0 {
				return false
			}
			d.quickUI.historyOpen = true
			d.selectQuickTurn(len(d.quickConversation) - 1)
			return true
		}
		index := d.quickSelectedTurnIndex()
		if index < 0 {
			index = len(d.quickConversation) - 1
		}
		d.selectQuickTurn(max(0, min(index-direction, len(d.quickConversation)-1)))
		return true
	}
	var value string
	var ok bool
	if direction < 0 {
		value, ok = d.quickHistory.Previous(d.quickInput)
	} else {
		value, ok = d.quickHistory.Next()
	}
	if ok {
		d.setQuickHistoryInput(value)
	}
	return ok
}

func (d *desktop) selectQuickTurn(index int) {
	if index < 0 || index >= len(d.quickConversation) {
		return
	}
	if d.quickConversation[index].navigationID == nil {
		d.quickConversation[index].navigationID = new(byte)
	}
	turn := d.quickConversation[index]
	d.quickUI.selectedTurn = &turn
	if d.app != nil {
		d.resizeQuickPanel(false)
		d.app.RequestUpdate()
		d.app.Post(d.revealQuickSelection)
	}
}

func (d *desktop) revealQuickSelection() {
	index := d.quickSelectedTurnIndex()
	if d.app == nil || index < 0 {
		return
	}
	scroll, ok := d.app.FindWidget("quick", "quick-scroll").(*gui.ScrollView)
	if !ok || scroll.Child() == nil {
		return
	}
	question := d.app.FindWidget("quick", fmt.Sprintf("quick-question-%d", index))
	if rect, ok := quickRectInAncestor(question, scroll.Child()); ok {
		scroll.ScrollIntoView(rect)
	}
}

func quickRectInAncestor(widget, ancestor gui.Widget) (geometry.Rectangle, bool) {
	if widget == nil || ancestor == nil {
		return geometry.Rectangle{}, false
	}
	rect := widget.Rect()
	for parent := widget.Parent(); parent != nil; parent = parent.Parent() {
		if parent == ancestor {
			return rect, true
		}
		bounds := parent.Rect()
		rect.X += bounds.X
		rect.Y += bounds.Y
	}
	return geometry.Rectangle{}, false
}

func (d *desktop) quickSelectedTurnIndex() int {
	if d == nil || d.quickUI.selectedTurn == nil {
		return -1
	}
	selected := d.quickUI.selectedTurn
	for i := len(d.quickConversation) - 1; i >= 0; i-- {
		turn := d.quickConversation[i]
		if selected.navigationID != nil && turn.navigationID == selected.navigationID {
			return i
		}
	}
	return -1
}

func (d *desktop) quickCopyText() (string, bool) {
	if d == nil || strings.TrimSpace(d.quickInput) != "" || len(d.quickVisibleTurns()) == 0 {
		return "", false
	}
	index := d.quickSelectedTurnIndex()
	if index < 0 {
		index = len(d.quickConversation) - 1
	}
	answer := d.quickConversation[index].answer
	return answer, strings.TrimSpace(answer) != ""
}

func (d *desktop) quickActivationPreferred() bool {
	return d.quickPanelPinned || d.quickPreview || d.quickUI.preferQuick
}

func (d *desktop) quickNoteSaveText() string {
	if d == nil {
		return ""
	}
	if strings.TrimSpace(d.quickInput) != "" {
		return quickPanelNoteText(d.quickInput)
	}
	if len(d.quickVisibleTurns()) == 0 {
		return ""
	}
	index := d.quickSelectedTurnIndex()
	if index < 0 {
		index = len(d.quickConversation) - 1
	}
	if index < 0 {
		return ""
	}
	turn := d.quickConversation[index]
	if turn.action {
		return ""
	}
	return strings.TrimSpace(turn.question + "\n\n" + turn.answer)
}

func (d *desktop) quickNoteSaveAvailable() bool {
	return d != nil && !d.loading && !d.askLoading && d.quickNoteSaveText() != ""
}

func (d *desktop) saveQuickSelectionAsNote() {
	if !d.quickNoteSaveAvailable() {
		return
	}
	// Saving is an explicit write even when the text looks like a question or
	// calculation. Keep it in the composer if the normal note save fails.
	d.quickInput = d.quickNoteSaveText()
	d.quickSaveNote()
}
