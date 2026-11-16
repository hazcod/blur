package darwin

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego/objc"
)

func TestMeetingRecordingStatusIconStaysOpaqueIndependentlyOfCapture(t *testing.T) {
	for _, capture := range []bool{false, true} {
		if got := menuBarStatusIconAlpha(capture, true); got != 1 {
			t.Fatalf("meeting indicator faded with passive capture=%t: %v", capture, got)
		}
	}
	if menuBarStatusIconAlpha(false, false) != menuBarIconAlpha(false) || menuBarStatusIconAlpha(true, false) != menuBarIconAlpha(true) {
		t.Fatal("inactive meeting changed capture icon appearance")
	}
}
func TestRecordingDotBoundsMatchPackagedCenterWithoutTouchingRing(t *testing.T) {
	for _, size := range []float64{20, 40, 80, 1024} {
		r := statusCenterDotRect(size)
		if math.Abs(r.X+r.Width/2-size/2) > 1e-9 || r.Width != r.Height || math.Abs(r.Width-size*132/1024) > 1e-9 || r.X < 0 || r.X+r.Width > size {
			t.Fatal("recording color escaped the original center-dot geometry")
		}
	}
}
func statusTestPNG(t *testing.T, native objc.ID) image.Image {
	t.Helper()
	data := objc.Send[objc.ID](native, objc.RegisterName("representationUsingType:properties:"), uint64(nsBitmapImageFileTypePNG), objc.ID(0))
	if data == 0 {
		t.Fatal("offscreen bitmap PNG unavailable")
	}
	count := objc.Send[uint64](data, objc.RegisterName("length"))
	pointer := objc.Send[unsafe.Pointer](data, objc.RegisterName("bytes"))
	if count == 0 || count > 1<<20 || pointer == nil {
		t.Fatal("invalid offscreen image data")
	}
	raw := append([]byte(nil), unsafe.Slice((*byte)(pointer), int(count))...) // #nosec G103 -- NSData owns this bounded memory during the autorelease pool.
	value, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func TestNativeRecordingIconChangesOnlyCopiedImageCenter(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if !loadStatusIconDrawing() {
		t.Fatal("native image framework unavailable")
	}
	withAutoreleasePool(func() {
		fixture := image.NewNRGBA(image.Rect(0, 0, 80, 80))
		for y := 0; y < 80; y++ {
			for x := 0; x < 80; x++ {
				pixel := color.NRGBA{R: 32, G: 37, B: 44, A: 255}
				distance := math.Hypot(float64(x)+0.5-40, float64(y)+0.5-40)
				if distance > 20 && distance < 26 {
					pixel = color.NRGBA{R: 210, G: 242, B: 223, A: 255}
				}
				if distance < 80*66.0/1024 {
					pixel = color.NRGBA{R: 167, G: 231, B: 193, A: 255}
				}
				fixture.SetNRGBA(x, y, pixel)
			}
		}
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, fixture); err != nil {
			t.Fatal(err)
		}
		raw := encoded.Bytes()
		nsData := objc.Send[objc.ID](objc.ID(objc.GetClass("NSData")), objc.RegisterName("dataWithBytes:length:"), unsafe.Pointer(&raw[0]), uint64(len(raw))) // #nosec G103 -- NSData synchronously copies synthetic PNG bytes.
		source := objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(objc.GetClass("NSImage")), objc.RegisterName("alloc")), objc.RegisterName("initWithData:"), nsData)
		runtime.KeepAlive(raw)
		if source == 0 {
			t.Fatal("synthetic native image unavailable")
		}
		defer releaseNativeObject(source)
		baseline := renderStatusIconBitmap(source, 80, false)
		recording := renderStatusIconBitmap(source, 80, true)
		again := renderStatusIconBitmap(source, 80, false)
		if baseline == 0 || recording == 0 || again == 0 {
			t.Fatal("offscreen native drawing unavailable")
		}
		defer releaseNativeObject(baseline)
		defer releaseNativeObject(recording)
		defer releaseNativeObject(again)
		before, red, unchanged := statusTestPNG(t, baseline), statusTestPNG(t, recording), statusTestPNG(t, again)
		for y := 0; y < 80; y++ {
			for x := 0; x < 80; x++ {
				a := color.NRGBAModel.Convert(before.At(x, y)).(color.NRGBA)
				b := color.NRGBAModel.Convert(red.At(x, y)).(color.NRGBA)
				c := color.NRGBAModel.Convert(unchanged.At(x, y)).(color.NRGBA)
				if a != c {
					t.Fatal("recording drawing mutated shared source icon")
				}
				if math.Abs(float64(x)+0.5-40) > 80*66.0/1024+1 || math.Abs(float64(y)+0.5-40) > 80*66.0/1024+1 {
					if a != b {
						t.Fatal("recording tint changed the ring/background outside original center dot")
					}
				}
			}
		}
		r, g, b, a := red.At(40, 40).RGBA()
		if r <= g*2 || r <= b*2 || a != 65535 {
			t.Fatal("center dot not opaque red")
		}
		private := newRecordingStatusIcon(source)
		if private == 0 || private == source {
			t.Fatal("recording image was not private")
		}
		defer releaseNativeObject(private)
		if objc.Send[bool](private, objc.RegisterName("isTemplate")) {
			t.Fatal("whole application icon became a template tint")
		}
	})
}

func TestRecordingFlagSurvivesHiddenMenuAndClearsBeforeRecreation(t *testing.T) {
	if statusBarItem != 0 || statusBarButton != 0 || statusMeetingItem != 0 || statusBarBaseImage != 0 || statusBarRecordingImage != 0 {
		t.Fatal("test must not operate a live status item")
	}
	previousOpen, previousRecording, previousControls := statusOpenMeeting, statusMeetingRecording, statusMeetingControls
	quick, settings, toggle, pause, quit := statusQuickCapture, statusOpenSettings, statusToggleCapture, statusPauseFor, statusQuit
	t.Cleanup(func() {
		statusOpenMeeting, statusMeetingRecording = previousOpen, previousRecording
		statusMeetingControls = previousControls
		statusQuickCapture, statusOpenSettings, statusToggleCapture, statusPauseFor, statusQuit = quick, settings, toggle, pause, quit
	})
	if err := ConfigureMeetingMenu(func() {}, true); err != nil {
		t.Fatal(err)
	}
	removeMenuBarItem() // all native handles are zero: models hide/reconstruction only
	if !statusMeetingRecording || menuBarStatusIconAlpha(false, statusMeetingRecording) != 1 {
		t.Fatal("hiding menu lost active meeting indicator")
	}
	rows := menuBarItemsWithMeetings(false, time.Time{}, time.Now(), false)
	if rows[3].title != "Pause recording" || rows[0].title != "Recording - Capture paused" {
		t.Fatal("recreated menu would lose recording state")
	}
	if err := ConfigureMeetingMenu(func() {}, false); err != nil {
		t.Fatal(err)
	}
	rows = menuBarItemsWithMeetings(false, time.Time{}, time.Now(), false)
	if statusMeetingRecording || rows[3].title != "Voice notes" || menuBarStatusIconAlpha(false, statusMeetingRecording) >= 1 {
		t.Fatal("paused/stopped hidden state would recreate stale red indicator")
	}
}
