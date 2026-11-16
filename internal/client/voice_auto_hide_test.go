package client

import (
	"testing"
	"time"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/ui"
)

type voiceAutoHideTimer struct {
	delay time.Duration
	run   func()
}

type voiceAutoHideApp struct {
	quickDashboardApp
	timers []voiceAutoHideTimer
}

func (a *voiceAutoHideApp) TimeoutFunc(delay time.Duration, run func()) *ui.Timer {
	a.timers = append(a.timers, voiceAutoHideTimer{delay, run})
	return nil
}

func TestVoiceOverlayAutoHidesAfterTenSecondsWithoutStoppingAudio(t *testing.T) {
	app := &voiceAutoHideApp{}
	d := &desktop{app: app, meetingUI: meetingViewState{overlayOpen: true, overlayPresented: true, live: MeetingLiveView{State: "running", AudioRecording: true}}}
	d.armMeetingOverlayAutoHide()
	d.armMeetingOverlayAutoHide()
	if len(app.timers) != 1 || app.timers[0].delay != 10*time.Second {
		t.Fatal("recording events restart the visibility deadline", app.timers)
	}
	app.timers[0].run()
	if d.meetingUI.overlayOpen || !d.meetingUI.live.AudioRecording || d.meetingUI.stopRequested {
		t.Fatal("auto-hide stopped audio or left the overlay visible")
	}
}

func TestVoiceOverlayPinInvalidatesDeadlineAndUnpinArmsFreshDeadline(t *testing.T) {
	app := &voiceAutoHideApp{}
	d := &desktop{app: app, meetingUI: meetingViewState{overlayOpen: true, overlayPresented: true, live: MeetingLiveView{State: "running", AudioRecording: true}}}
	d.armMeetingOverlayAutoHide()
	d.toggleMeetingOverlayPin()
	app.timers[0].run()
	if !d.meetingUI.overlayOpen || !d.meetingUI.overlayPinned {
		t.Fatal("a stale timer hid the pinned overlay")
	}
	d.toggleMeetingOverlayPin()
	if len(app.timers) != 2 || app.timers[1].delay != 10*time.Second {
		t.Fatal("unpin did not start a fresh visibility deadline")
	}
	app.timers[0].run()
	if !d.meetingUI.overlayOpen {
		t.Fatal("old deadline hid a newly unpinned overlay")
	}
	app.timers[1].run()
	if d.meetingUI.overlayOpen {
		t.Fatal("unpinned overlay did not auto-hide")
	}
}

func TestVoiceOverlayDoesNotAutoHideSetupOrFailure(t *testing.T) {
	for _, state := range []string{"starting", "failed", "completed", "ready"} {
		app := &voiceAutoHideApp{}
		d := &desktop{app: app, meetingUI: meetingViewState{overlayOpen: true, overlayPresented: true, live: MeetingLiveView{State: state}}}
		d.armMeetingOverlayAutoHide()
		if len(app.timers) != 0 {
			t.Fatalf("%s recovery/setup message could disappear", state)
		}
	}
}

type voicePhraseLayoutProbe struct {
	width float32
	rect  geometry.Rectangle
}

func (p *voicePhraseLayoutProbe) Measure(layout.Constraint) layout.Measurement {
	return layout.Measured(geometry.Size{Width: p.width, Height: 20})
}
func (p *voicePhraseLayoutProbe) Arrange(rect geometry.Rectangle) { p.rect = rect }
func (*voicePhraseLayoutProbe) MainWeight() float32               { return 0 }

func TestVoicePhraseSlidesOnlyOverflowingWordsToTheEnd(t *testing.T) {
	for _, tc := range []struct{ width, x float32 }{{100, 0}, {280, 0}, {500, -220}} {
		child := &voicePhraseLayoutProbe{width: tc.width}
		voicePhraseLayout{}.Arrange([]layout.Child{child}, geometry.Rect(0, 0, 280, 20))
		if child.rect.X != tc.x || child.rect.Height != 20 || child.rect.Width < 280 {
			t.Fatal("spoken words escaped or did not slide within the line", child.rect)
		}
	}
}
