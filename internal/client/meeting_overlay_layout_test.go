package client

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/platform"
	"github.com/golang-gui/goui/ui"
)

type voiceLayoutWindow struct {
	gui.Window
	shown bool
}

func (w *voiceLayoutWindow) Show() error                   { w.shown = true; return nil }
func (*voiceLayoutWindow) SetStyleName(string)             {}
func (*voiceLayoutWindow) PlatformWindow() platform.Window { return nil }

type voiceLayoutWidget struct {
	gui.Widget
	t      *testing.T
	window *voiceLayoutWindow
}

func (w *voiceLayoutWidget) Measure(layout.Constraint) layout.Measurement {
	if !w.window.shown {
		w.t.Error("voice overlay measured a hidden window before its first Show")
	}
	return layout.Measurement{Height: 222}
}

type voiceLayoutApp struct {
	quickDashboardApp
	window  *voiceLayoutWindow
	widget  gui.Widget
	lookups int
	posted  chan func()
}

func (a *voiceLayoutApp) FindWindow(id string) gui.Window {
	if id == "meeting" {
		return a.window
	}
	return nil
}
func (a *voiceLayoutApp) Post(fn func()) {
	if a.posted == nil {
		fn()
		return
	}
	a.posted <- fn
}
func (a *voiceLayoutApp) next(t *testing.T) {
	t.Helper()
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("voice overlay did not complete its posted operation")
	}
}
func (a *voiceLayoutApp) FindWidget(string, string) gui.Widget {
	a.lookups++
	return a.widget
}

func TestVoiceOverlayDeclarationDoesNotLookUpMountedWidgets(t *testing.T) {
	app := &voiceLayoutApp{}
	d := &desktop{app: app}
	_ = d.meetingOverlayWindow()
	if app.lookups != 0 {
		t.Fatal("window declaration synchronously looked up live widgets during rebuild")
	}
}

func TestVoiceOverlayPreservesCreationOptionsAcrossEveryContentUpdate(t *testing.T) {
	d := &desktop{app: &voiceLayoutApp{}, meetingUI: meetingViewState{overlayOpen: true}}
	// GoUI's windowMount.update compares this exact field before reconciling
	// content. A change returns the creation-only error and ends ui.Run.
	// Reflection reads/comparisons need no unsafe access or native window.
	options := func(window ui.WindowView) reflect.Value {
		value := reflect.ValueOf(window).FieldByName("options")
		if !value.IsValid() || !value.Comparable() {
			t.Fatal("GoUI creation-options contract changed; update this regression")
		}
		return value
	}
	initial := options(d.meetingOverlayWindow())
	for _, state := range []struct {
		name   string
		update func()
	}{
		{"starting", func() { d.meetingUI.busy = true; d.meetingUI.live.State = "starting" }},
		{"setup error", func() {
			d.meetingUI.status = "Apple speech files are missing."
			d.meetingUI.live.Issue = "assets_missing"
		}},
		{"options opened", func() { d.meetingUI.optionsOpen = true }},
		{"note editor opened", func() { d.meetingUI.notesOpen = true; d.meetingUI.draft = "A thought" }},
		{"running", func() {
			d.meetingUI.status = ""
			d.meetingUI.live = MeetingLiveView{State: "running", AudioRecording: true}
			d.meetingUI.busy = false
		}},
		{"paused", func() { d.meetingUI.live.State = "paused" }},
		{"closed", func() { d.meetingUI.overlayOpen = false; d.meetingUI.overlayPresented = false }},
		{"reopened", func() { d.meetingUI.overlayOpen = true; d.meetingUI.notesOpen = false; d.meetingUI.optionsOpen = false }},
	} {
		t.Run(state.name, func(t *testing.T) {
			state.update()
			if !initial.Equal(options(d.meetingOverlayWindow())) {
				t.Fatal(`window "meeting": Size, Chrome, Transparent, SkipTaskbar and Center are creation-only; content update would stop the desktop runtime`)
			}
		})
	}
}

func TestVoiceOverlayMeasuresOnlyAfterShow(t *testing.T) {
	window := &voiceLayoutWindow{}
	app := &voiceLayoutApp{window: window, widget: &voiceLayoutWidget{t: t, window: window}}
	d := &desktop{app: app, meetingUI: meetingViewState{overlayOpen: true}}
	d.presentMeetingOverlay(0)
	if !window.shown || app.lookups == 0 {
		t.Fatal("overlay did not show before fitting mounted content")
	}
}

func TestVoiceOverlayMenuStartsRecordingAfterShowingOnce(t *testing.T) {
	f := newControllerFixture(t)
	window := &voiceLayoutWindow{}
	app := &voiceLayoutApp{window: window, posted: make(chan func(), 16)}
	d := &desktop{app: app, client: f.c, meetingUI: meetingViewState{
		locale: "en-US", microphone: true, loaded: true, probing: true,
	}}
	started := make(chan bool, 2)
	var starts atomic.Int32
	f.audio.onStart = func(context.Context) {
		starts.Add(1)
		started <- window.shown
	}
	d.openMeetingsFromMenu()
	app.next(t) // The menu action requests presentation, without blocking UI.
	if window.shown || starts.Load() != 0 {
		t.Fatal("recording or presentation ran before its UI turn")
	}
	app.next(t) // Show, then begin native setup on the operation worker.
	select {
	case shown := <-started:
		if !shown {
			t.Fatal("recording setup blocked the first visible frame")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("opening Voice notes still requires a second Record action")
	}
	app.next(t) // Complete the start receipt.
	d.openMeetingsFromMenu()
	app.next(t)
	app.next(t)
	if starts.Load() != 1 {
		t.Fatal("reopening a running overlay started a duplicate recording")
	}
}

func TestVoiceNotesSidebarOpensNotesPane(t *testing.T) {
	app := &dashboardPresentApp{}
	d := &desktop{app: app, page: "Settings"}
	d.activateSidebarPage("Voice notes")
	if d.meetingUI.overlayOpen || d.meetingUI.overlayMounted || d.page != "Ask" {
		t.Fatal("Voice notes sidebar action did not open the notes pane")
	}
}
