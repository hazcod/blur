package client

import (
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/platform/events"
	"github.com/golang-gui/goui/ui"
)

// Empty focused text inputs consume Command-C even with no selection. Capture
// the answer-copy gesture before that editor, while keeping draft copy native.
func (d *desktop) newMemoryKeyController() *gui.KeyEventController {
	controller := gui.NewKeyEventController()
	controller.SetPhase(gui.PhaseCapture)
	controller.ConnectKeyDown(func(ctx gui.EventContext, event events.KeyEvent) {
		if d.handleMemoryKey(event) {
			ctx.StopPropagation()
			event.PreventDefault()
		}
	})
	return controller
}

func (d *desktop) watchMemoryKeyboard() {
	widget := d.app.FindWidget("main", "memory-composer-root")
	u := &d.memoryUI
	if widget == nil || widget == u.keyboardWidget && u.keyboardController != nil {
		return
	}
	if u.keyboardWidget != nil && u.keyboardController != nil {
		u.keyboardWidget.RemoveEventController(u.keyboardController)
	}
	u.keyboardWidget, u.keyboardController = widget, d.newMemoryKeyController()
	widget.AddEventController(u.keyboardController)
}

type memoryKeyboardMountView struct {
	ui.ComposeBase[memoryKeyboardMountView]
	desktop *desktop
	content ui.View
}

func memoryKeyboardMount(d *desktop, content ui.View) ui.View {
	v := &memoryKeyboardMountView{desktop: d, content: content}
	v.Self = v
	return v
}

func (v *memoryKeyboardMountView) Build(ctx ui.BuildContext) ui.View {
	ctx.AfterUpdate(v.desktop.watchMemoryKeyboard)
	return v.content
}
