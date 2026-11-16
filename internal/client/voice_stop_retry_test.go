package client

import (
	"context"
	"testing"
	"time"
)

func TestVoicePopupStopFailureCannotPoisonNextRecording(t *testing.T) {
	for _, publishFailure := range []bool{true, false} {
		name := "before_failure_reaches_ui"
		if publishFailure {
			name = "after_failure_reaches_ui"
		}
		t.Run(name, func(t *testing.T) {
			f := newControllerFixture(t)
			f.audio.stream.autoComplete.Store(false)
			app := &meetingOperationTestApp{posted: make(chan func(), 16)}
			d := &desktop{client: f.c, app: app, meetingUI: meetingViewState{overlayOpen: true, locale: "en-US", microphone: true}}
			f.start(t)
			d.meetingUI.live = f.c.MeetingLive()
			d.stopMeetingFromOverlay()
			app.finish(t)
			if !d.meetingUI.stopRequested || !d.meetingUI.stopDispatching {
				t.Fatal("fixture did not retain accepted Stop pending native completion")
			}
			f.c.meetings.gate.Lock()
			f.c.meetings.fail(context.Background(), f.c.meetings.active, "audio_interrupted")
			f.c.meetings.gate.Unlock()
			if publishFailure {
				d.publishMeetingLive(f.c.MeetingLive())
			}
			// Native resize callbacks share this UI queue with receipts.
			for len(app.posted) > 0 {
				app.finish(t)
			}
			fresh := newControllerFixture(t)
			f.c.meetings.gate.Lock()
			f.c.meetings.audio = fresh.audio
			f.c.meetings.gate.Unlock()
			d.startMeeting()
			app.finish(t)
			if d.meetingUI.busy || f.c.MeetingLive().State != "starting" {
				t.Fatal("retry did not start a fresh recording")
			}
			d.meetingUI.live = f.c.MeetingLive()
			d.stopMeetingFromOverlay()
			select {
			case control := <-fresh.audio.stream.controls:
				if control.operation != "stop" {
					t.Fatal("retry dispatched an incorrect control", control)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("failed Stop suppressed the next recording's Stop")
			}
			app.finish(t)
		})
	}
}
