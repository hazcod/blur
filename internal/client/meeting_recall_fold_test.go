package client

import (
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
)

func TestVoiceRecallTopicMatchFoldsAccentedMeetingText(t *testing.T) {
	topics := map[string]bool{"orfit": true, "reunion": true}
	for _, text := range []string{"Réunion Orfit", "RÉUNION", "réunion kickoff", "Orfit's budget"} {
		if !voiceRecallTopicMatch(text, topics) {
			t.Fatalf("%q did not match folded topics", text)
		}
	}
	if voiceRecallTopicMatch("Weekly planning", topics) {
		t.Fatal("unrelated text matched")
	}
}

func TestAccentedMeetingTitleMatchesFoldedQuestion(t *testing.T) {
	f := newControllerFixture(t)
	f.c.askNow = func() time.Time { return f.now.Add(5 * time.Minute) }
	if err := f.c.StartMeeting(t.Context(), "Réunion Hebdo", "en-US", []string{"system"}); err != nil {
		t.Fatal(err)
	}
	id := f.c.MeetingLive().Meeting.ID
	words := controllerWords(id, 1, "final", "We agreed to ship the budget review on Friday.", true)
	words.Track = "system"
	f.audio.stream.emit(controllerState(id, 1, "starting"), controllerState(id, 1, "running"), words)
	waitController(t, f.c, func(v MeetingLiveView) bool { return len(v.Tail) == 1 })
	if err := f.c.ControlMeeting(t.Context(), "stop"); err != nil {
		t.Fatal(err)
	}
	waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "completed" })
	plan, err := assistant.PlanQuestion("what did we say in the meeting about the reunion", f.c.AskTime(), nil)
	if err != nil || !asksRecordedContent(plan) {
		t.Fatalf("question did not select recorded content: %+v err=%v", plan, err)
	}
	result, err := f.c.RetrievePlan(t.Context(), plan)
	if err != nil || len(result.Evidence) != 1 || !strings.Contains(result.Evidence[0].Text, "budget review") {
		t.Fatalf("accented meeting title was not matched by the folded question: result=%+v err=%v", result, err)
	}
}
