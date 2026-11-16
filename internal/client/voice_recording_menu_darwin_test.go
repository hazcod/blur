package client

import "testing"

// A probe or operation completion can install the newest live snapshot before
// the coalesced recording event reaches publishMeetingLive. The menu still has
// to reconcile recording authority, even when the displayed state is unchanged.
func TestVoiceRecordingMenuReconcilesUnchangedLiveState(t *testing.T) {
	app := &voiceMeterApp{}
	d := &desktop{app: app, meetingUI: meetingViewState{
		live:      MeetingLiveView{State: "running", AudioRecording: true},
		menuIssue: "Previous menu update failed",
	}}
	d.publishMeetingLive(MeetingLiveView{State: "running", AudioRecording: true})
	if d.meetingUI.menuIssue != "" {
		t.Fatal("unchanged live state skipped the recording menu update")
	}

	d.meetingUI.menuIssue = "Previous menu update failed"
	d.publishMeetingLive(MeetingLiveView{State: "running", AudioRecording: false})
	if d.meetingUI.menuIssue != "" {
		t.Fatal("recording acknowledgement changed without refreshing the menu")
	}
}
