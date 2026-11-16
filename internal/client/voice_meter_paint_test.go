package client

import (
	"bytes"
	"image"
	"image/draw"
	"testing"
	"time"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/platform/graphics"
)

// Only the native upload target is substituted. The actual declarative meter,
// DrawingArea cache policy and Software Painter generate every pixel.
type voiceMeterPixelTarget struct {
	gui.Painter
	pixels      *image.RGBA
	generations int
}

type voiceMeterPixelImage struct{ target *voiceMeterPixelTarget }

func (i *voiceMeterPixelImage) Size() (int, int) {
	return i.target.pixels.Bounds().Dx(), i.target.pixels.Bounds().Dy()
}
func (i *voiceMeterPixelImage) Update(src image.Image) error {
	draw.Draw(i.target.pixels, i.target.pixels.Bounds(), src, src.Bounds().Min, draw.Src)
	i.target.generations++
	return nil
}
func (*voiceMeterPixelImage) Destroy() {}

func (*voiceMeterPixelTarget) PixelScale() float32                          { return 1 }
func (*voiceMeterPixelTarget) Save()                                        {}
func (*voiceMeterPixelTarget) Restore()                                     {}
func (*voiceMeterPixelTarget) SetClipRect(geometry.Rectangle)               {}
func (*voiceMeterPixelTarget) DrawImage(geometry.Rectangle, graphics.Image) {}
func (p *voiceMeterPixelTarget) NewImage(src image.Image) (graphics.Image, error) {
	p.pixels = image.NewRGBA(image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()))
	draw.Draw(p.pixels, p.pixels.Bounds(), src, src.Bounds().Min, draw.Src)
	p.generations++
	return &voiceMeterPixelImage{target: p}, nil
}

func TestVoiceMeterRepaintsActualPixelsForSignalAndSilence(t *testing.T) {
	d := &desktop{meetingUI: meetingViewState{live: MeetingLiveView{State: "running"}}}
	widget, ok := mountVoicePopupView(t, d.meetingAudioMeter()).(*gui.DrawingArea)
	if !ok {
		t.Fatal("audio meter is not a real DrawingArea")
	}
	widget.Arrange(geometry.Rect(0, 0, 48, 20))
	target := &voiceMeterPixelTarget{}
	paint := func() []byte {
		t.Helper()
		widget.RequestPaint()
		widget.Paint(target)
		if target.pixels == nil {
			t.Fatal("meter did not generate pixels")
		}
		return bytes.Clone(target.pixels.Pix)
	}
	silent := paint()
	d.meetingUI.live.Levels = map[string]MeetingAudioLevel{"microphone": {Value: 0.005, At: time.Now()}}
	quiet := paint()
	if bytes.Equal(quiet, silent) {
		t.Fatal("fresh quiet speech repainted the cached silent bitmap")
	}
	d.meetingUI.live.Levels["microphone"] = MeetingAudioLevel{Value: 0.5, At: time.Now()}
	loud := paint()
	if bytes.Equal(loud, quiet) {
		t.Fatal("louder speech did not change the actual meter pixels")
	}
	d.meetingUI.live.Levels["microphone"] = MeetingAudioLevel{Value: 0.5, At: time.Now().Add(-time.Second)}
	if stale := paint(); !bytes.Equal(stale, silent) {
		t.Fatal("expired audio retained an active meter bitmap")
	}
	d.meetingUI.live.Levels["microphone"] = MeetingAudioLevel{Value: 0.5, At: time.Now()}
	d.meetingUI.live.State = "paused"
	if paused := paint(); !bytes.Equal(paused, silent) {
		t.Fatal("paused audio retained an active meter bitmap")
	}
	if target.generations != 5 {
		t.Fatal("RequestPaint did not regenerate each meter frame", target.generations)
	}
}
