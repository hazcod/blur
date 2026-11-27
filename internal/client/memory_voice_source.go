package client

import (
	"context"

	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/meetings"
)

type meetingSourceReader interface {
	MeetingForSource(context.Context, string) (meetings.Meeting, bool, error)
}

func (d *desktop) memoryVoiceSourceAction(sourceID, appID string) ui.View {
	if d.client == nil || appID != "sh.blur.meetings" {
		return nil
	}
	if _, ok := d.client.data.(meetingSourceReader); !ok {
		return nil
	}
	return hug(flatButton("Read voice note").Enabled(!d.meetingUI.reading).OnClick(func() { d.readMemoryVoiceSource(sourceID) }))
}

func (d *desktop) readMemoryVoiceSource(sourceID string) {
	reader, ok := d.client.data.(meetingSourceReader)
	if !ok {
		return
	}
	read, stable := d.client.beginVoicePublication()
	if !stable {
		d.status = voiceReadChangedCopy
		d.app.RequestUpdate()
		return
	}
	u := &d.memoryUI
	u.voiceSourceRequest++
	request, generation := u.voiceSourceRequest, d.memoryGeneration
	d.meetingUI.reading = true
	d.app.RequestUpdate()
	go func() {
		ctx, done, err := d.client.beginOperation(context.Background(), shortOperationTimeout)
		if err != nil {
			d.app.Post(func() {
				if request == u.voiceSourceRequest {
					d.meetingUI.reading = false
					d.app.RequestUpdate()
				}
			})
			return
		}
		defer done()
		meeting, found, err := reader.MeetingForSource(ctx, sourceID)
		d.app.Post(func() {
			if request != u.voiceSourceRequest || generation != d.memoryGeneration {
				return
			}
			d.meetingUI.reading = false
			if !read.publish(d.client, func() {
				if err != nil || !found {
					d.status = "This voice note is no longer available."
				} else {
					d.selectMemoryMeeting(meeting)
				}
			}) {
				d.status = voiceReadChangedCopy
				d.app.RequestUpdate()
				return
			}
			if err == nil && found {
				d.loadMeetingTranscript(0)
			}
			d.app.RequestUpdate()
		})
	}()
}
