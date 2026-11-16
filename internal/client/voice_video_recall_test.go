package client

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/meetings"
)

func savedVoiceVideoFixture(t *testing.T) (controllerFixture, string) {
	t.Helper()
	f := newControllerFixture(t)
	f.c.askNow = func() time.Time { return f.now.Add(5 * time.Minute) }
	if err := f.c.StartMeeting(t.Context(), "Voice notes", "en-US", []string{"system"}); err != nil {
		t.Fatal(err)
	}
	id := f.c.MeetingLive().Meeting.ID
	words := controllerWords(id, 1, "video-final", "The video explains TCP congestion control. Slow start grows the congestion window quickly; packet loss reduces the sending rate.", true)
	words.Track = "system"
	f.audio.stream.emit(controllerState(id, 1, "starting"), controllerState(id, 1, "running"), words)
	waitController(t, f.c, func(v MeetingLiveView) bool { return len(v.Tail) == 1 })
	if err := f.c.ControlMeeting(t.Context(), "stop"); err != nil {
		t.Fatal(err)
	}
	waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "completed" })
	transcript, err := f.c.MeetingTranscript(t.Context(), id, 0)
	if err != nil || len(transcript.Records) != 1 {
		t.Fatal("synthetic voice transcript was not saved", err)
	}
	screen, err := capture.SanitizeObservation("com.apple.Safari", "youtube.com", "TCP congestion control explained - YouTube. Like Share Subscribe.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.PutObservationAt(t.Context(), screen, f.now); err != nil {
		t.Fatal(err)
	}
	return f, transcript.Records[0].SourceID
}

func TestVideoRecallIncludesRecentSavedVoiceOutsideWebsiteScope(t *testing.T) {
	f, voiceID := savedVoiceVideoFixture(t)
	apps, err := f.c.KnownApps(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := assistant.PlanQuestion("What was my youtube video about?", f.c.AskTime(), apps)
	if err != nil || len(plan.Domains) != 1 || plan.Domains[0] != "youtube.com" {
		t.Fatal("synthetic website scope was not recognized", plan, err)
	}
	result, err := f.c.RetrievePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, evidence := range result.Evidence {
		if evidence.SourceID == voiceID {
			if len(result.Evidence) != 1 || !result.Diagnosis.VoiceAssociationUnverified || !evidence.AssociationUnverified || evidence.AppID != "sh.blur.meetings" || !strings.HasSuffix(evidence.Domain, ".meeting.local") || evidence.Domain == "youtube.com" || !strings.Contains(evidence.Text, "congestion") {
				t.Fatal("voice recall changed genuine source provenance")
			}
			return
		}
	}
	t.Fatal("website-scoped video question excluded the saved voice transcript")
}

func TestVideoRecallDoesNotPresentBrowserChromeAsSpokenContent(t *testing.T) {
	f, _ := savedVoiceVideoFixture(t)
	f.c.askNow = func() time.Time { return f.now.Add(6 * time.Hour) }
	result, err := f.c.AskMemory(t.Context(), "What was my youtube video about?")
	if err != nil || !result.Answer.InsufficientEvidence || len(result.Sources) != 0 || !strings.Contains(result.Answer.Text, "saved voice transcript") || strings.Contains(result.Answer.Text, "Like Share Subscribe") {
		t.Fatal("out-of-window voice recall fell back to browser playback controls", err)
	}
}

func TestVideoLinkLookupKeepsWebsiteSearchInsteadOfSelectingAudio(t *testing.T) {
	f, voiceID := savedVoiceVideoFixture(t)
	apps, err := f.c.KnownApps(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := assistant.PlanQuestion("Find my YouTube video link about congestion", f.c.AskTime(), apps)
	if err != nil || asksRecordedContent(plan) {
		t.Fatal("a link lookup was incorrectly interpreted as spoken-content recall", err)
	}
	result, err := f.c.RetrievePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, evidence := range result.Evidence {
		if evidence.SourceID == voiceID {
			t.Fatal("a link lookup selected unverified recording content")
		}
	}
}

func TestNaturalMeetingQuestionRecallsVoiceTranscriptWithoutBrowserChrome(t *testing.T) {
	for _, question := range []string{
		"What did we discuss in the meeting about TCP congestion control?",
		"What did we talk about in the meeting about TCP congestion control?",
		"What was said in the meeting about TCP congestion control?",
		"What did we discuss in the meeting?",
		"Give me a recap of the meeting about TCP congestion control.",
		"What were the action items from the meeting about TCP congestion control?",
		"What were the key points from our meeting about TCP congestion control?",
		"What outcomes did we agree on in the meeting about TCP congestion control?",
	} {
		t.Run(question, func(t *testing.T) {
			f, voiceID := savedVoiceVideoFixture(t)
			plan, err := assistant.PlanQuestion(question, f.c.AskTime(), nil)
			if err != nil || !asksRecordedContent(plan) {
				t.Fatalf("natural meeting question did not select transcript recall: plan=%+v err=%v", plan, err)
			}
			result, err := f.c.RetrievePlan(t.Context(), plan)
			if err != nil || len(result.Evidence) != 1 || result.Evidence[0].SourceID != voiceID || !strings.Contains(strings.ToLower(result.Evidence[0].Text), "congestion") || !result.Diagnosis.VoiceRequested || result.Diagnosis.VoiceAssociationUnverified || result.Evidence[0].AssociationUnverified {
				t.Fatalf("meeting recall missed the saved transcript or used browser chrome: result=%+v err=%v", result, err)
			}
			answer, err := f.c.AskMemory(t.Context(), question)
			if err != nil || len(answer.Sources) != 1 || answer.Sources[0].SourceID != voiceID || !strings.Contains(strings.ToLower(answer.Answer.Text), "congestion") || strings.Contains(strings.ToLower(answer.Answer.Text), "like share subscribe") {
				t.Fatalf("meeting question did not produce transcript-grounded output: answer=%+v err=%v", answer, err)
			}
		})
	}
}

func TestMeetingTimeQuestionDoesNotSelectTranscriptRecall(t *testing.T) {
	plan, err := assistant.PlanQuestion("When is my next meeting with Mira?", time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if asksRecordedContent(plan) {
		t.Fatalf("meeting scheduling question was mistaken for recorded-content recall: %+v", plan)
	}
}

func TestNaturalConversationSourceQuestionsSelectRecordedContent(t *testing.T) {
	for _, question := range []string{
		"What did Mira agree to in the call about TCP congestion control?",
		"What commitments did we make during the standup about TCP congestion control?",
		"Who was assigned to review TCP congestion control in the sync?",
		"What did we decide during the session about TCP congestion control?",
		"What did the speaker say in the interview about TCP congestion control?",
		"What were the key points from the webinar about TCP congestion control?",
		"What did the presenter cover in the lecture about TCP congestion control?",
		"What did they discuss at the conference about TCP congestion control?",
		"What was the podcast episode about TCP congestion control?",
	} {
		plan, err := assistant.PlanQuestion(question, time.Now(), nil)
		if err != nil || !asksRecordedContent(plan) {
			t.Errorf("conversation-source question missed transcript recall: question=%q plan=%+v err=%v", question, plan, err)
		}
	}
	for _, question := range []string{"When is my next call?", "When is the next team sync?", "When is the next conference?"} {
		plan, err := assistant.PlanQuestion(question, time.Now(), nil)
		if err != nil || asksRecordedContent(plan) {
			t.Errorf("scheduling question was mistaken for recorded content: question=%q plan=%+v err=%v", question, plan, err)
		}
	}
}

func TestMeetingRecallUsesSavedMeetingInsteadOfShortVideoAssociationHorizon(t *testing.T) {
	f, voiceID := savedVoiceVideoFixture(t)
	f.c.askNow = func() time.Time { return f.now.Add(6 * time.Hour) }
	question := "What did we discuss in the meeting about TCP congestion control?"
	plan, err := assistant.PlanQuestion(question, f.c.AskTime(), nil)
	if err != nil || !asksRecordedContent(plan) {
		t.Fatalf("meeting question did not select transcript retrieval: %+v err=%v", plan, err)
	}
	result, err := f.c.RetrievePlan(t.Context(), plan)
	if err != nil || len(result.Evidence) != 1 || result.Evidence[0].SourceID != voiceID || !strings.Contains(strings.ToLower(result.Evidence[0].Text), "congestion") {
		t.Fatalf("named meeting recall incorrectly used the short video horizon: result=%+v err=%v", result, err)
	}
}

func TestTopicMeetingRecallSearchesPastRecentMeetingPage(t *testing.T) {
	f, voiceID := savedVoiceVideoFixture(t)
	for i := 0; i < 6; i++ {
		id, err := f.c.NewMeetingNotes(t.Context(), fmt.Sprintf("Unrelated meeting %d", i), "en-US", []string{"system"})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.c.AddMeetingNote(t.Context(), id, meetings.ManualNoteInput{ID: "agenda", RecordedAt: f.now.Add(time.Duration(i) * time.Minute), Text: "Discuss the unrelated office move."}); err != nil {
			t.Fatal(err)
		}
	}
	question := "What did we discuss in the meeting about TCP congestion control?"
	plan, err := assistant.PlanQuestion(question, f.c.AskTime(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.c.RetrievePlan(t.Context(), plan)
	if err != nil || len(result.Evidence) != 1 || result.Evidence[0].SourceID != voiceID || !strings.Contains(strings.ToLower(result.Evidence[0].Text), "congestion") {
		t.Fatalf("topic search stopped at the recent-meetings page: result=%+v err=%v", result, err)
	}
}

func TestVideoRecallNativeSyntheticEncryptedTranscript(t *testing.T) {
	if os.Getenv("BLUR_TEST_APPLE_HELPER") == "" {
		t.Skip("requires the canonical helper beside the test binary")
	}
	f, voiceID := savedVoiceVideoFixture(t)
	model, err := assistant.NewAppleGenerator()
	if err != nil {
		t.Fatal(err)
	}
	service := assistant.NewService(f.c, model, model, assistant.ContextTokenLimit)
	result, err := service.Ask(t.Context(), "What was my youtube video about?")
	if err != nil || result.Direct || result.GenerationIssue != "" || result.Answer.InsufficientEvidence || len(result.Sources) != 1 || result.Sources[0].SourceID != voiceID || !strings.Contains(strings.ToLower(result.Answer.Text), "congestion") || strings.Contains(result.Answer.Text, "Like Share Subscribe") || !result.CoverageLimited {
		t.Fatalf("native voice answer lost transcript authority: sources=%d direct=%t issue=%s limited=%t error=%v", len(result.Sources), result.Direct, result.GenerationIssue, result.CoverageLimited, err)
	}
}

func TestVoiceRecallDoesNotSubstituteAnUnrelatedRecentRecording(t *testing.T) {
	f, _ := savedVoiceVideoFixture(t)
	result, err := f.c.AskMemory(t.Context(), "What did my recording about alpinephotography say?")
	if err != nil || !result.Answer.InsufficientEvidence || len(result.Sources) != 0 {
		t.Fatal("named recording topic was replaced by unrelated recent speech", err)
	}
}
