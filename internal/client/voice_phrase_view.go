package client

import (
	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/ui"
)

// The parent bounds clip a real native label. New words move the line toward
// its end without scrollbars, additional windows, or continuous polling.
type voicePhraseViewport struct {
	gui.WidgetBase
	child gui.Widget
}

func (w *voicePhraseViewport) Child() gui.Widget { return w.child }
func (w *voicePhraseViewport) SetChild(child gui.Widget) {
	if w.child == child {
		return
	}
	w.RemoveChild(w.child)
	w.child = child
	w.AddChild(w, child)
}

type voicePhraseLayout struct{}

func (voicePhraseLayout) Measure(children []layout.Child, c layout.Constraint) layout.Measurement {
	if len(children) == 0 {
		return layout.Measurement{}
	}
	measured := children[0].Measure(layout.Unbounded())
	return layout.Measured(c.Clamp(geometry.Size{Width: measured.Width, Height: 20}))
}

func (voicePhraseLayout) Arrange(children []layout.Child, rect geometry.Rectangle) {
	if len(children) == 0 {
		return
	}
	width := max(rect.Width, children[0].Measure(layout.Unbounded()).Width)
	children[0].Arrange(geometry.Rect(rect.Width-width, 0, width, rect.Height))
}

type voicePhraseViewportView struct {
	ui.ViewBase[voicePhraseViewportView]
	child ui.View
}

func newVoicePhraseViewport(phrase string) ui.View {
	v := &voicePhraseViewportView{child: ui.Label(phrase).WrapMode(ui.WrapNone).Style(styleQuickRow)}
	v.Self = v
	return v.MinHeight(20).MaxHeight(20).MainWeight(1)
}

func (*voicePhraseViewportView) Mount(ui.BuildContext) gui.Widget {
	w := &voicePhraseViewport{}
	w.SetLayoutManager(voicePhraseLayout{})
	return w
}

func (v *voicePhraseViewportView) Update(ctx ui.BuildContext, w gui.Widget) {
	ctx.UpdateChild(w.(*voicePhraseViewport), v.child)
}

func (*voicePhraseViewportView) Unmount(ui.BuildContext, gui.Widget) {}
