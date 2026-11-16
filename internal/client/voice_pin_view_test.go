package client

import (
	"bytes"
	"image"
	"image/draw"
	"testing"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/platform/events"
	"github.com/golang-gui/goui/platform/graphics"
	"github.com/golang-gui/goui/ui"
)

func TestVoicePinClickChangesCachedIconAndUnpinRestoresAutoHide(t *testing.T) {
	for _, scale := range []float32{1, 2} {
		app := &voiceAutoHideApp{}
		d := &desktop{app: app, meetingUI: meetingViewState{overlayOpen: true, overlayPresented: true, live: MeetingLiveView{State: "running", AudioRecording: true}}}
		d.armMeetingOverlayAutoHide()
		mounted := &voicePinMountedView{t: t}
		button := mounted.update(d.voicePinButton()).(*gui.Button)
		button.Arrange(geometry.Rect(0, 0, 32, 32))
		icon := button.Child().(*gui.DrawingArea)
		icon.Arrange(geometry.Rect(6, 6, 20, 20))
		painter := &voicePinPainter{scale: scale}
		icon.Paint(painter)
		initial := append([]byte(nil), painter.pixels.Pix...)
		if bytes.Equal(initial, make([]byte, len(initial))) {
			t.Fatal("pin icon is invisible")
		}
		host := &quickHistoryKeyHost{root: button}
		dispatch := &gui.EventDispatcher{}
		click := func() {
			t.Helper()
			for _, kind := range []events.EventType{events.PointerDown, events.PointerUp} {
				if err := dispatch.DispatchEvent(host, events.PointerEvent{EventType: kind, Button: events.PointerButtonLeft, Position: geometry.Point{X: 16, Y: 16}}); err != nil {
					t.Fatal(err)
				}
			}
			if mounted.update(d.voicePinButton()) != button || button.Child() != icon {
				t.Fatal("pin toggling replaced the mounted control")
			}
			icon.Paint(painter)
		}
		click()
		if !d.meetingUI.overlayPinned || bytes.Equal(initial, painter.pixels.Pix) {
			t.Fatal("pin click did not visibly change the cached icon")
		}
		app.timers[0].run()
		if !d.meetingUI.overlayOpen {
			t.Fatal("old timer hid pinned recording controls")
		}
		click()
		if d.meetingUI.overlayPinned || !bytes.Equal(initial, painter.pixels.Pix) || len(app.timers) != 2 {
			t.Fatal("unpin did not restore the icon and auto-hide deadline")
		}
		app.timers[1].run()
		if d.meetingUI.overlayOpen || !d.meetingUI.live.AudioRecording {
			t.Fatal("unpin auto-hide affected audio or left controls visible")
		}
	}
}

// Preserve real declarative widget state across updates, including the drawing
// area's cached pixels. Remount-only tests cannot catch a stale icon.
type voicePinMountedView struct {
	ui.BuildContext
	t      *testing.T
	state  any
	widget gui.Widget
	view   ui.WidgetView
	child  *voicePinMountedView
}

func (m *voicePinMountedView) State() any         { return m.state }
func (m *voicePinMountedView) SetState(state any) { m.state = state }
func (m *voicePinMountedView) update(view ui.View) gui.Widget {
	m.view = view.(ui.WidgetView)
	if m.widget == nil {
		m.widget = m.view.Mount(m)
		m.t.Cleanup(func() { m.view.Unmount(m, m.widget) })
	}
	m.view.Update(m, m.widget)
	return m.widget
}
func (m *voicePinMountedView) UpdateChild(target ui.Bin, view ui.View, apply ...func(gui.Widget)) gui.Widget {
	if m.child == nil {
		m.child = &voicePinMountedView{t: m.t}
	}
	w := m.child.update(view)
	target.SetChild(w)
	for _, fn := range apply {
		fn(w)
	}
	return w
}

type voicePinPainter struct {
	gui.Painter
	scale  float32
	pixels *image.RGBA
}

func (p *voicePinPainter) PixelScale() float32                        { return p.scale }
func (*voicePinPainter) Save()                                        {}
func (*voicePinPainter) Restore()                                     {}
func (*voicePinPainter) SetClipRect(geometry.Rectangle)               {}
func (*voicePinPainter) DrawImage(geometry.Rectangle, graphics.Image) {}
func (p *voicePinPainter) NewImage(src image.Image) (graphics.Image, error) {
	i := &voicePinImage{p: p}
	return i, i.Update(src)
}

type voicePinImage struct{ p *voicePinPainter }

func (i *voicePinImage) Size() (int, int) { return i.p.pixels.Bounds().Dx(), i.p.pixels.Bounds().Dy() }
func (*voicePinImage) Destroy()           {}
func (i *voicePinImage) Update(src image.Image) error {
	i.p.pixels = image.NewRGBA(image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()))
	draw.Draw(i.p.pixels, i.p.pixels.Bounds(), src, src.Bounds().Min, draw.Src)
	return nil
}
