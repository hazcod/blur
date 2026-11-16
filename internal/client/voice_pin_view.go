package client

import (
	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/platform/graphics"
	"github.com/golang-gui/goui/ui"
)

func (d *desktop) voicePinButton() ui.View {
	pinned := d.meetingUI.overlayPinned
	name := "Pin"
	if pinned {
		name = "Unpin"
	}
	icon := &voicePinIconView{pinned: pinned}
	icon.Self = icon
	return ui.Button().Content(icon.MinSize(20, 20).MaxSize(20, 20)).ID("voice-overlay-pin").
		Name(name).Tooltip(name+" recording controls").Style(styleMeetingAction).
		MinSize(28, 28).Paddings(geometry.Insets{Left: 4, Right: 4, Top: 4, Bottom: 4}).OnClick(d.toggleMeetingOverlayPin)
}

// DrawingArea caches its generated pixels. Updating a draw closure alone does
// not invalidate them; replace the draw function only when the pin state changes.
type voicePinIconView struct {
	ui.ViewBase[voicePinIconView]
	pinned bool
}

func (*voicePinIconView) Mount(ui.BuildContext) gui.Widget {
	return gui.NewDrawingArea(nil)
}

func (v *voicePinIconView) Update(ctx ui.BuildContext, widget gui.Widget) {
	if previous, initialized := ctx.State().(bool); initialized && previous == v.pinned {
		return
	}
	ctx.SetState(v.pinned)
	pinned := v.pinned
	widget.(*gui.DrawingArea).SetDrawFunc(func(p gui.Painter, _ geometry.Size) {
		q := quickPalette()
		ink := graphics.ColorOf(q.inkMuted)
		if pinned {
			ink = graphics.ColorOf(q.amber)
			p.FillRect(geometry.Rect(7, 4, 6, 5), ink)
		}
		for _, segment := range [][4]float32{{6, 4, 14, 4}, {7, 4, 7, 9}, {13, 4, 13, 9}, {7, 9, 5, 12}, {13, 9, 15, 12}, {5, 12, 15, 12}, {10, 12, 10, 18}} {
			p.DrawLine(geometry.Point{X: segment[0], Y: segment[1]}, geometry.Point{X: segment[2], Y: segment[3]}, 1.5, ink)
		}
		if pinned {
			p.DrawLine(geometry.Point{X: 3, Y: 2}, geometry.Point{X: 18, Y: 18}, 1.5, ink)
		}
	})
}

func (*voicePinIconView) Unmount(ui.BuildContext, gui.Widget) {}
