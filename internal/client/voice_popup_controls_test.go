package client

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/platform/darwin"
)

func TestVoicePopupRunningHasNoRedundantRecordingSubtitle(t *testing.T) {
	f := newControllerFixture(t)
	d := &desktop{client: f.c, app: &quickDashboardApp{}, meetingUI: meetingViewState{live: MeetingLiveView{State: "running", AudioRecording: true}}}
	var check func(gui.Widget)
	check = func(w gui.Widget) {
		if label, ok := w.(*gui.Label); ok && strings.Contains(strings.ToLower(label.Text()), "transcribing on this mac") {
			t.Fatal("recording popup still includes the redundant subtitle")
		}
		for _, child := range w.Children() {
			check(child)
		}
	}
	check(mountVoicePopupView(t, d.meetingOverlay()))
}

func TestVoicePhraseUsesOneClippedLineWithoutScrollbars(t *testing.T) {
	d := &desktop{app: &quickDashboardApp{}, meetingUI: meetingViewState{live: MeetingLiveView{LivePhrase: strings.Repeat("wide spoken phrase ", 20)}}}
	d.updateVoiceLiveLabels(time.Now())
	w := mountVoicePopupView(t, d.voiceLivePhraseView())
	viewport, ok := w.(*voicePhraseViewport)
	if !ok {
		t.Fatalf("phrase needs a clipping viewport without scrollbars, got %T", w)
	}
	label, ok := viewport.Child().(*gui.Label)
	if !ok || label.WrapMode() != gui.WrapNone {
		t.Fatal("phrase can wrap onto another line")
	}
	viewport.Arrange(geometry.Rect(0, 0, 280, 20))
	if label.Parent() != viewport || len(viewport.Children()) != 1 {
		t.Fatal("phrase has scrollbars or lacks its clipping parent")
	}
	if viewport.Rect().Height != 20 {
		t.Fatal("phrase escaped its single-line height")
	}
}

// Mount the real declarative controls without a native app, storage reads or
// audio. This checks the rendered action tree, not a duplicate list of labels.
type voicePopupMountContext struct {
	ui.BuildContext
	t     *testing.T
	state any
}

func (c *voicePopupMountContext) State() any         { return c.state }
func (c *voicePopupMountContext) SetState(value any) { c.state = value }
func (c *voicePopupMountContext) AfterUpdate(func()) {}
func (c *voicePopupMountContext) RequestUpdate()     {}
func (c *voicePopupMountContext) UpdateChild(target ui.Bin, view ui.View, apply ...func(gui.Widget)) gui.Widget {
	w := mountVoicePopupView(c.t, view)
	target.SetChild(w)
	for _, fn := range apply {
		fn(w)
	}
	return w
}
func (c *voicePopupMountContext) UpdateChildren(target ui.Container, views []ui.View, apply ...func(int, gui.Widget)) []gui.Widget {
	widgets := make([]gui.Widget, len(views))
	for i, view := range views {
		widgets[i] = mountVoicePopupView(c.t, view)
		if widgets[i] != nil {
			target.AddChild(widgets[i])
		}
		for _, fn := range apply {
			fn(i, widgets[i])
		}
	}
	return widgets
}
func mountVoicePopupView(t *testing.T, view ui.View) gui.Widget {
	t.Helper()
	if view == nil {
		return nil
	}
	ctx := &voicePopupMountContext{t: t}
	if composed, ok := view.(ui.ComposeView); ok {
		return mountVoicePopupView(t, composed.Build(ctx))
	}
	widgetView, ok := view.(ui.WidgetView)
	if !ok {
		t.Fatalf("unsupported popup view %T", view)
	}
	w := widgetView.Mount(ctx)
	widgetView.Update(ctx, w)
	t.Cleanup(func() { widgetView.Unmount(ctx, w) })
	return w
}
func voicePopupButtonLabels(t *testing.T, d *desktop) []string {
	t.Helper()
	var labels []string
	var walk func(gui.Widget)
	walk = func(w gui.Widget) {
		if button, ok := w.(*gui.Button); ok {
			label, ok := button.Child().(*gui.Label)
			if !ok {
				// This lightweight mount does not apply ViewBase semantic names;
				// the header's only non-text control is the actual pin drawing.
				if _, icon := button.Child().(*gui.DrawingArea); icon {
					labels = append(labels, "Pin")
				} else {
					t.Fatalf("unexpected popup action content %T", button.Child())
				}
			} else {
				labels = append(labels, label.Text())
			}
		}
		for _, child := range w.Children() {
			walk(child)
		}
	}
	walk(mountVoicePopupView(t, d.meetingOverlay()))
	return labels
}

func TestVoicePopupShowsOnlyPauseAndStopDuringRecording(t *testing.T) {
	f := newControllerFixture(t)
	for _, state := range []string{"starting", "running", "paused", "pausing", "stopping"} {
		t.Run(state, func(t *testing.T) {
			d := &desktop{client: f.c, app: &quickDashboardApp{}, meetingUI: meetingViewState{live: MeetingLiveView{State: state}, microphone: true, optionsOpen: true, notesOpen: true}}
			want := []string{"Pin", "Pause", "Stop"}
			if state == "paused" {
				want[1] = "Resume"
			}
			if got := voicePopupButtonLabels(t, d); !reflect.DeepEqual(got, want) {
				t.Fatalf("popup actions = %q, want %q", got, want)
			}
		})
	}
}

func TestVoicePopupMissingAssetsAlwaysShowsItsDownloadAction(t *testing.T) {
	f := newControllerFixture(t)
	app := &meetingOperationTestApp{posted: make(chan func(), 8)}
	d := &desktop{client: f.c, app: app, meetingUI: meetingViewState{locale: "en-US", microphone: true}}
	// The native error can arrive before a complete availability snapshot.
	d.meetingOperation(func(context.Context) error { return darwin.MeetingAudioOperationError{Code: "assets_missing"} }, nil)
	app.finish(t)
	for _, state := range []string{"", "ready", "starting", "failed"} {
		d.meetingUI.live.State = state
		if got := voicePopupButtonLabels(t, d); !reflect.DeepEqual(got, []string{"Pin", "Download & start"}) {
			t.Fatalf("%q missing speech files have no matching recovery action: %q", state, got)
		}
	}
}

func TestVoicePopupStopCancelsPendingSetupWithoutStartingAudio(t *testing.T) {
	f := newControllerFixture(t)
	app := &meetingOperationTestApp{posted: make(chan func(), 8)}
	d := &desktop{client: f.c, app: app, meetingUI: meetingViewState{overlayOpen: true, startOnShow: true}}
	started := make(chan struct{})
	d.meetingOperation(func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() }, nil)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("setup did not start")
	}
	d.stopMeetingFromOverlay()
	app.finish(t)
	if d.meetingUI.overlayOpen || d.meetingUI.startOnShow || d.meetingUI.busy || d.meetingUI.stopRequested || d.meetingUI.status != "" || f.c.MeetingLive().AudioRecording {
		t.Fatal("Stop did not cancel and dismiss pending setup cleanly", d.meetingUI.status)
	}
}

func TestVoicePopupDismissesOnlyAfterStopIsAcknowledged(t *testing.T) {
	app := &voiceMeterApp{}
	d := &desktop{app: app, meetingUI: meetingViewState{overlayOpen: true, stopRequested: true, live: MeetingLiveView{State: "stopping", AudioRecording: true}}}
	d.publishMeetingLive(MeetingLiveView{State: "stopping", AudioRecording: true})
	if !d.meetingUI.overlayOpen {
		t.Fatal("popup hid before recording stopped")
	}
	d.publishMeetingLive(MeetingLiveView{State: "completed"})
	if d.meetingUI.overlayOpen || d.meetingUI.stopRequested {
		t.Fatal("completed Stop did not dismiss popup")
	}
}

func TestVoicePopupStopAfterNativeAdmissionBeforeUICompletionSendsStopOnce(t *testing.T) {
	f := newControllerFixture(t)
	app := &meetingOperationTestApp{posted: make(chan func(), 8)}
	d := &desktop{client: f.c, app: app, meetingUI: meetingViewState{overlayOpen: true, locale: "en-US", microphone: true}}
	d.startMeeting()
	var completion func()
	select {
	case completion = <-app.posted:
	case <-time.After(5 * time.Second):
		t.Fatal("native start did not reach UI completion")
	}
	d.stopMeetingFromOverlay()
	completion()
	select {
	case control := <-f.audio.stream.controls:
		if control.operation != "stop" {
			t.Fatal(control)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop canceled setup but left the admitted recording alive")
	}
	app.finish(t)
	if d.meetingUI.busy || !d.meetingUI.stopDispatching {
		t.Fatal("Stop completion started another operation")
	}
	select {
	case control := <-f.audio.stream.controls:
		t.Fatal("Stop completion dispatched another Stop", control)
	default:
	}
}

func TestVoicePopupHealthyRecordingClearsAnOlderSetupIssue(t *testing.T) {
	app := &voiceMeterApp{}
	d := &desktop{app: app, meetingUI: meetingViewState{overlayOpen: true, setupIssue: "assets_missing", status: meetingIssueCopy("assets_missing")}}
	d.publishMeetingLive(MeetingLiveView{State: "running", AudioRecording: true})
	if d.meetingUI.setupIssue != "" || d.meetingUI.status != "" {
		t.Fatal("healthy recording retained obsolete setup guidance")
	}
}
