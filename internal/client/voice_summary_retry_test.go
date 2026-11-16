package client

import (
	"context"
	"testing"

	"github.com/hazcod/blur/internal/meetings"
)

func TestVoiceSummaryRetryClearsPreviousFailureBeforeAndAfterSuccess(t *testing.T) {
	c, store, _, meeting := summaryFixture(t, 1)
	page, err := store.MeetingRecords(context.Background(), meeting.ID, meetings.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 8)}
	d := &desktop{client: c, app: app, meetingUI: meetingViewState{selected: meeting.ID, transcript: page, status: "The previous recap could not finish."}}
	d.summarizeMeetingPage()
	if d.meetingUI.status != "" || !d.meetingUI.summaryLoading {
		t.Fatal("retry still displays the old recap failure")
	}
	app.finish(t)
	if d.meetingUI.summary == nil || d.meetingUI.summaryLoading || d.meetingUI.status != "" {
		t.Fatal("successful recap is still accompanied by a stale error", d.meetingUI.status)
	}
}
