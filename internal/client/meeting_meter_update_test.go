package client

import (
	"testing"
	"time"

	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/ui"
)

type voiceMeterWidget struct {
	gui.Widget
	paints int
}

func (w *voiceMeterWidget) RequestPaint() { w.paints++ }

type voiceMeterApp struct {
	quickDashboardApp
	widget  voiceMeterWidget
	updates int
	timers  []func()
}

func (a *voiceMeterApp) RequestUpdate() { a.updates++ }
func (a *voiceMeterApp) FindWidget(window, id string) gui.Widget {
	if window == "meeting" && id == "meeting-audio-level" {
		return &a.widget
	}
	return nil
}
func (a *voiceMeterApp) TimeoutFunc(_ time.Duration, fn func()) *ui.Timer {
	a.timers = append(a.timers, fn)
	return nil
}

func TestVoiceMeterDoesNotTickWhenIdlePausedOrHidden(t *testing.T) {
	for _, state := range []string{"", "ready", "starting", "paused", "completed", "failed"} {
		app := &voiceMeterApp{}
		d := &desktop{app: app, meetingUI: meetingViewState{overlayOpen: true, live: MeetingLiveView{State: state}}}
		d.tickMeetingOverlay()
		if len(app.timers) != 0 {
			t.Errorf("%q overlay starts a recurring repaint timer", state)
		}
	}
	app := &voiceMeterApp{}
	d := &desktop{app: app, meetingUI: meetingViewState{live: MeetingLiveView{State: "running"}}}
	d.tickMeetingOverlay()
	if len(app.timers) != 0 {
		t.Fatal("hidden overlay starts a repaint timer")
	}
}

func TestVoiceMeterTickPaintsOnlyItsWidgetAndStopsAfterPause(t *testing.T) {
	app := &voiceMeterApp{}
	d := &desktop{app: app, meetingUI: meetingViewState{overlayOpen: true, live: MeetingLiveView{State: "running"}}}
	d.tickMeetingOverlay()
	if len(app.timers) != 1 {
		t.Fatal("running meter did not schedule its freshness tick")
	}
	app.timers[0]()
	if app.updates != 0 || app.widget.paints != 1 || len(app.timers) != 2 {
		t.Fatalf("meter rebuilt the app: updates=%d paints=%d timers=%d", app.updates, app.widget.paints, len(app.timers))
	}
	d.meetingUI.live.State = "paused"
	app.timers[1]()
	if app.updates != 0 || len(app.timers) != 2 || d.meetingUI.overlayTickRunning {
		t.Fatal("paused overlay continued its repaint loop")
	}
}

func TestVoiceLevelEventsDoNotRebuildViews(t *testing.T) {
	app := &voiceMeterApp{}
	d := &desktop{app: app, meetingUI: meetingViewState{overlayOpen: true, live: MeetingLiveView{State: "running"}}}
	next := d.meetingUI.live
	next.Levels = map[string]MeetingAudioLevel{"microphone": {Value: 0.4, At: time.Now()}}
	d.publishMeetingLive(next)
	if app.updates != 0 || app.widget.paints != 1 || len(app.timers) != 1 {
		t.Fatalf("level-only event rebuilt views: updates=%d paints=%d timers=%d", app.updates, app.widget.paints, len(app.timers))
	}
	next.Preview = map[string]string{"microphone": "A live thought"}
	d.publishMeetingLive(next)
	if app.updates != 0 || d.meetingUI.livePhraseLabel.Peek() != "A live thought" {
		t.Fatal("speech preview did not update its own label without rebuilding the app")
	}
	next.Issue = "assets_missing"
	d.publishMeetingLive(next)
	if app.updates != 1 {
		t.Fatal("new transcription issue did not update the view")
	}
}

func TestHiddenVoiceHistoryDoesNotRebuildForLivePhrase(t *testing.T) {
	app := &voiceMeterApp{}
	d := &desktop{app: app, page: "Voice notes", dashboardOpen: false, meetingUI: meetingViewState{overlayOpen: true, live: MeetingLiveView{State: "running"}}}
	next := d.meetingUI.live
	next.LivePhrase = "The current phrase"
	d.publishMeetingLive(next)
	if app.updates != 0 || d.meetingUI.livePhraseLabel.Peek() != next.LivePhrase {
		t.Fatal("hidden history forced an app-wide rebuild")
	}
	d.dashboardOpen = true
	d.page, d.memoryUI.meetingDetail = "Ask", true
	next.LivePhrase = "A newer phrase"
	d.publishMeetingLive(next)
	if app.updates != 1 {
		t.Fatal("visible saved history stopped receiving transcript updates")
	}
}
