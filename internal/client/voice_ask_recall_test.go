package client

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestVoiceFinalCanBeReadAndAskedAfterStopWithoutCaptureOrModel(t *testing.T) {
	f := newControllerFixture(t)
	id := f.start(t)
	const text = "The rollback owner is Mira."
	f.audio.stream.emit(controllerState(id, 1, "running"), controllerWords(id, 1, "recall-final", text, true))
	waitController(t, f.c, func(v MeetingLiveView) bool { return len(v.Tail) == 1 })
	if err := f.c.ControlMeeting(context.Background(), "stop"); err != nil {
		t.Fatal(err)
	}
	waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "completed" })
	page, err := f.c.MeetingPage(context.Background(), 0)
	if err != nil || len(page.Meetings) != 1 || page.Meetings[0].ID != id {
		t.Fatal("completed recording is absent from saved history", err)
	}
	transcript, err := f.c.MeetingTranscript(context.Background(), id, 0)
	if err != nil || len(transcript.Records) != 1 || transcript.Records[0].Record.Event.Text != text {
		t.Fatal("saved transcript is missing the final phrase", err)
	}
	f.c.SetCaptureStateReader(func() (bool, time.Time) { return false, f.now })
	answer, err := f.c.AskMemory(context.Background(), "Who is Mira?")
	if err != nil || !strings.Contains(answer.Answer.Text, text) || len(answer.Sources) != 1 || answer.Sources[0].SourceID != transcript.Records[0].SourceID || answer.SentItems != 0 {
		t.Fatal("Ask could not recall the saved final without capture or a model", err)
	}
}
