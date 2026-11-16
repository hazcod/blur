package darwin

import (
	"testing"
	"time"
)

func TestMeetingMenuHasSeparateWorkspaceActionAndRecordingState(t *testing.T) {
	previous, recording, controls := statusOpenMeeting, statusMeetingRecording, statusMeetingControls
	t.Cleanup(func() {
		statusOpenMeeting, statusMeetingRecording, statusMeetingControls = previous, recording, controls
	})
	var opened, stopped int
	statusOpenMeeting, statusMeetingRecording = func() { opened++ }, true
	statusMeetingControls = MeetingMenuControls{Active: true, Stop: func() { stopped++ }}
	items := menuBarItemsWithMeetings(true, time.Time{}, time.Now(), true)
	if items[0].title != "Recording - Capturing" || items[2].target != statusTargetInteract || items[3].target != statusTargetMeeting || items[3].selector != "openMeeting:" || !items[3].enabled || items[3].title != "Pause recording" || items[4].title != "Stop recording" || items[4].hidden || items[5].target != statusTargetSettings {
		t.Fatal("note action missing or displaced existing actions")
	}
	dispatchStatusMeeting()
	if opened != 1 {
		t.Fatal("note action did not open workspace")
	}
	dispatchStatusMeetingStop()
	if stopped != 1 {
		t.Fatal("Stop recording did not dispatch its explicit action")
	}
	statusMeetingRecording, statusMeetingControls.Paused = false, true
	items = menuBarItemsWithMeetings(true, time.Time{}, time.Now(), true)
	if items[3].title != "Resume recording" || items[4].hidden {
		t.Fatal("paused recording lost Resume or Stop")
	}
	statusOpenMeeting, statusMeetingRecording = nil, false
	statusMeetingControls = MeetingMenuControls{}
	items = menuBarItemsWithMeetings(false, time.Time{}, time.Now(), false)
	if items[3].title != "Voice notes" || items[3].enabled || !items[4].hidden {
		t.Fatal("unconfigured note action should remain visible and disabled")
	}
}

func TestRecordingStatusPreservesPausedCaptureAndCommandOnlyDetail(t *testing.T) {
	now := time.Now()
	if got := menuBarStatusTitle(true, false, false, time.Time{}, now); got != "Recording - Capture paused" {
		t.Fatal(got)
	}
	if got := menuBarStatusTitle(true, true, false, time.Time{}, now); got != "Recording - Capturing" {
		t.Fatal("compact status leaked capture substatus", got)
	}
	if got := menuBarStatusTitle(true, true, false, time.Time{}, now, "waiting for readable content"); got != "Recording - Capturing; waiting for readable content" {
		t.Fatal("Command detail or recording status missing", got)
	}
}
