package client

import (
	"testing"
	"time"

	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/meetings"
)

type voiceMenuTimer struct {
	delay time.Duration
	run   func()
}

// Post remains queued, as in the real desktop runtime. In particular, a menu
// action and the asynchronous operation receipt must execute in separate turns.
type voiceMenuTestApp struct {
	meetingOperationTestApp
	timers []voiceMenuTimer
}

func (a *voiceMenuTestApp) TimeoutFunc(delay time.Duration, fn func()) *ui.Timer {
	a.timers = append(a.timers, voiceMenuTimer{delay: delay, run: fn})
	return nil
}

func (a *voiceMenuTestApp) fireTimer(t *testing.T, delay time.Duration) {
	t.Helper()
	for i, timer := range a.timers {
		if timer.delay == delay {
			a.timers = append(a.timers[:i], a.timers[i+1:]...)
			timer.run()
			return
		}
	}
	t.Fatal("voice reveal timer was not scheduled")
}

func newVoiceMenuDesktop(t *testing.T) (controllerFixture, *desktop, *voiceMenuTestApp, string) {
	t.Helper()
	f := newControllerFixture(t)
	id := f.start(t)
	f.audio.stream.emit(controllerState(id, 1, "starting"), controllerState(id, 1, "running"))
	live := waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "running" })
	a := &voiceMenuTestApp{meetingOperationTestApp: meetingOperationTestApp{posted: make(chan func(), 64)}}
	d := &desktop{client: f.c, app: a, meetingUI: meetingViewState{
		live: live, loaded: true, probing: true, probedAt: time.Now(), locale: "en-US",
	}}
	return f, d, a, id
}

func assertVoiceMenuDidNotOpenUI(t *testing.T, d *desktop) {
	t.Helper()
	if d.meetingUI.overlayOpen || d.meetingUI.overlayMounted || d.quickPanelPinned || d.quickPreview || d.dashboardOpen {
		t.Fatal("recording menu control opened a window")
	}
}

func assertVoiceMenuHasNoNativeControl(t *testing.T, f controllerFixture) {
	t.Helper()
	select {
	case command := <-f.audio.stream.controls:
		t.Fatalf("visibility change sent native control: %s", command.operation)
	default:
	}
	select {
	case <-f.audio.stream.closed:
		t.Fatal("visibility change closed native recording")
	default:
	}
}

func TestVoiceOverlayHideKeepsRecordingAndEncryptedFinalAdmission(t *testing.T) {
	f, d, _, id := newVoiceMenuDesktop(t)
	d.meetingUI.overlayOpen, d.meetingUI.overlayPresented, d.meetingUI.startOnShow = true, true, true
	d.hideMeetingOverlay()
	if d.meetingUI.overlayOpen || d.meetingUI.overlayPresented || d.meetingUI.startOnShow {
		t.Fatal("hide left overlay eligible for presentation")
	}
	assertVoiceMenuHasNoNativeControl(t, f)
	if live := f.c.MeetingLive(); live.State != "running" || !live.AudioRecording {
		t.Fatal("hide changed actual recording state")
	}
	f.audio.stream.emit(controllerWords(id, 1, "after-hide", "The hidden recording remains active.", true))
	waitController(t, f.c, func(v MeetingLiveView) bool { return len(v.Tail) == 1 })
	if rows := controllerRecords(t, f, id); len(rows) != 1 || rows[0].Record.Event.Text != "The hidden recording remains active." {
		t.Fatal("hidden recording did not save its final transcript")
	}
}

func TestVoiceMenuPauseResumeUsesNativeEpochWithoutOpeningUI(t *testing.T) {
	f, d, a, id := newVoiceMenuDesktop(t)
	d.meetingMenuAction()
	assertVoiceMenuHasNoNativeControl(t, f)
	a.finish(t)
	if command := controllerControl(t, f.audio.stream); command != (controllerAudioControl{"pause", 1}) {
		t.Fatalf("Pause menu sent incorrect native command: %+v", command)
	}
	f.audio.stream.emit(controllerState(id, 2, "pausing"), controllerState(id, 2, "paused"))
	waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "paused" })
	a.finish(t)
	assertVoiceMenuDidNotOpenUI(t, d)
	if d.meetingUI.busy || d.meetingUI.live.AudioRecording {
		t.Fatal("Pause menu did not publish the native acknowledgement")
	}

	d.meetingMenuAction()
	a.finish(t)
	if command := controllerControl(t, f.audio.stream); command != (controllerAudioControl{"resume", 2}) {
		t.Fatalf("Resume menu sent incorrect native command: %+v", command)
	}
	f.audio.stream.emit(controllerState(id, 3, "starting"), controllerState(id, 3, "running"))
	waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "running" })
	a.finish(t)
	assertVoiceMenuDidNotOpenUI(t, d)
	if d.meetingUI.busy || !d.meetingUI.live.AudioRecording {
		t.Fatal("Resume menu did not publish the native acknowledgement")
	}
}

func TestVoiceMenuStopDrainsEncryptedFinalWithoutOpeningUI(t *testing.T) {
	f, d, a, id := newVoiceMenuDesktop(t)
	f.audio.stream.autoComplete.Store(false)
	d.stopMeetingFromMenu()
	assertVoiceMenuHasNoNativeControl(t, f)
	a.finish(t)
	if command := controllerControl(t, f.audio.stream); command != (controllerAudioControl{"stop", 1}) {
		t.Fatalf("Stop menu sent incorrect native command: %+v", command)
	}
	a.finish(t)
	assertVoiceMenuDidNotOpenUI(t, d)
	if !d.meetingUI.stopRequested || !d.meetingUI.stopDispatching {
		t.Fatal("Stop menu lost its pending native drain")
	}
	f.audio.stream.emit(controllerState(id, 1, "stopping"), controllerWords(id, 1, "menu-stop-final", "Save these final words before stopping.", true), controllerState(id, 2, "completed"))
	live := waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "completed" })
	d.publishMeetingLive(live)
	assertVoiceMenuDidNotOpenUI(t, d)
	if d.meetingUI.stopRequested || d.meetingUI.stopDispatching || live.AudioRecording {
		t.Fatal("Stop menu retained a recording claim after native completion")
	}
	if rows := controllerRecords(t, f, id); len(rows) != 1 || rows[0].Record.Event.Text != "Save these final words before stopping." {
		t.Fatal("Stop menu discarded the final encrypted transcript")
	}
	metadata, found, err := f.store.GetMeeting(t.Context(), id)
	if err != nil || !found || metadata.State != meetings.Stopped || metadata.RecordCount != 1 {
		t.Fatal("Stop menu did not seal the saved voice note")
	}
}

func TestVoiceNotchHoverRevealsCurrentRecordingAndHidesQuick(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		name := "preview"
		if pinned {
			name = "pinned"
		}
		t.Run(name, func(t *testing.T) {
			f, d, a, id := newVoiceMenuDesktop(t)
			d.quickPanelPinned, d.quickPreview, d.quickPanelMounted = pinned, true, true
			d.onNotchHover(true)
			a.finish(t)
			a.fireTimer(t, quickPanelNotchDelay)
			a.finish(t)
			if !d.meetingUI.overlayOpen || !d.meetingUI.overlayMounted || d.meetingUI.startOnShow || d.quickPanelPinned || d.quickPreview {
				t.Fatal("notch hover did not reveal the active voice overlay exclusively")
			}
			if d.meetingUI.live.Meeting.ID != id || d.meetingUI.live.State != "running" {
				t.Fatal("notch hover did not reopen the current recording")
			}
			assertVoiceMenuHasNoNativeControl(t, f)
			if live := f.c.MeetingLive(); live.Meeting.ID != id || !live.AudioRecording {
				t.Fatal("notch reveal replaced or stopped the recording")
			}
		})
	}
}

func TestVoiceNotchRevealCannotStartNewRecordingAfterNativeCompletion(t *testing.T) {
	f, d, a, id := newVoiceMenuDesktop(t)
	d.onNotchHover(true)
	a.finish(t)
	a.fireTimer(t, quickPanelNotchDelay)
	// The timer has requested a reveal, but its posted UI turn has not run.
	// Native completion can arrive during that gap independently of the UI.
	if err := f.c.ControlMeeting(t.Context(), "stop"); err != nil {
		t.Fatal(err)
	}
	if command := controllerControl(t, f.audio.stream); command != (controllerAudioControl{"stop", 1}) {
		t.Fatalf("fixture sent incorrect native completion control: %+v", command)
	}
	waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "completed" })
	a.finish(t)
	if d.meetingUI.startOnShow {
		t.Fatal("stale notch reveal armed a new recording after native completion")
	}
	if live := f.c.MeetingLive(); live.Meeting.ID != id || live.State != "completed" {
		t.Fatal("stale notch reveal replaced the completed recording")
	}
	select {
	case command := <-f.audio.stream.controls:
		t.Fatalf("stale notch reveal sent another native control: %s", command.operation)
	default:
	}
}
