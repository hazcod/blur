package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/reminders"
)

const meetingPasteFixture = "# Platform planning\nAttendees: Maya, Niels, Jonas\nDecisions:\n- Keep the original source bytes.\nAction items:\n- Maya: Send the revised plan on Monday.\n- Jonas said: \"Remind me to review the draft tomorrow at 9am.\"\nNext review: Friday at noon.\n"

type meetingPasteAnswerModel struct{ *singleSearchModel }

func (g meetingPasteAnswerModel) GenerateGeneral(ctx context.Context, question string, outputTokens int) (string, error) {
	return g.GenerateGeneralPrompt(ctx, "Answer the supplied request.", question, outputTokens)
}

type meetingPasteApp struct{ *pausedAskApp }

func (*meetingPasteApp) Sync(f func()) { f() }

func TestMeetingPasteLocalRouteBypassesInvalidIntentModel(t *testing.T) {
	for _, test := range []struct{ name, input, want string }{
		{"raw minutes", meetingPasteFixture, meetingPasteFixture},
		{"raw whitespace", "\n\t" + meetingPasteFixture + "\n  ", "\n\t" + meetingPasteFixture + "\n  "},
		{"explicit note", "Save a note:\n" + meetingPasteFixture, meetingPasteFixture},
		{"explicit plural", "Save these meeting notes:\n" + meetingPasteFixture, meetingPasteFixture},
		{"CRLF paste", strings.ReplaceAll(meetingPasteFixture, "\n", "\r\n"), strings.ReplaceAll(meetingPasteFixture, "\n", "\r\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			g := &singleSearchModel{respond: func(context.Context, string, string) (string, error) { return "", nil }}
			c := selectedEntryClient(t, ProviderApple, false, g)
			resolved, err := c.ResolveEntry(context.Background(), test.input, EntryState{}, "UTC")
			if err != nil || resolved.Action.Kind != intent.Note || resolved.Action.Text != test.want || resolved.Draft != nil || resolved.Target != nil || g.calls != 0 {
				t.Fatalf("meeting paste was interpreted as executable content or changed: kind=%s bytes=%d want=%d draft=%+v target=%+v modelCalls=%d err=%v", resolved.Action.Kind, len(resolved.Action.Text), len(test.want), resolved.Draft, resolved.Target, g.calls, err)
			}
		})
	}
}

func TestMeetingPasteSupersedesPendingReminderWithoutBecomingItsTime(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		t.Fatal("fixture memory not stable")
	}
	state := EntryState{Epoch: epoch, Pending: &reminders.Draft{Title: "call the dentist", When: "tomorrow", Missing: "time"}}
	resolved, err := c.ResolveEntry(context.Background(), meetingPasteFixture, state, "UTC")
	if err != nil || resolved.Action.Kind != intent.Note || resolved.Action.Text != meetingPasteFixture || resolved.Draft != nil {
		t.Fatalf("paste became timing for old reminder: %+v err=%v", resolved, err)
	}
}

func TestMeetingPasteOversizeAnalysisClarifiesWithoutSaving(t *testing.T) {
	for _, prefix := range []string{
		"Summarize these meeting notes in five bullets:",
		"Could you please extract the action items as text without creating tasks?",
		"Translate these notes into Dutch:",
		"Rewrite these rough minutes into a clear summary:",
		"What decisions were made in these pasted notes?",
		"List the unresolved questions from this transcript:",
		"Explain the tradeoffs mentioned in this meeting:",
	} {
		t.Run(prefix, func(t *testing.T) {
			input := prefix + "\n\n" + meetingPasteFixture + strings.Repeat("Discussion: keep the evidence with the decision.\n", 180)
			g := &singleSearchModel{respond: func(context.Context, string, string) (string, error) { return `{"kind":"note"}`, nil }}
			c := selectedEntryClient(t, ProviderApple, false, g)
			resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
			if err != nil || resolved.Action.Kind != intent.Clarify || !strings.Contains(resolved.Message, "too large") || resolved.Draft != nil || g.calls != 0 {
				t.Fatalf("oversize analysis was saved or its unsupported size was hidden: kind=%s message=%q modelCalls=%d err=%v", resolved.Action.Kind, resolved.Message, g.calls, err)
			}
		})
	}
}

func TestMeetingPasteShortAnalysisUsesActualGeneralAnswerPath(t *testing.T) {
	input := "Summarize these meeting notes:\n\n" + meetingPasteFixture
	g := &singleSearchModel{respond: func(_ context.Context, _, user string) (string, error) {
		if !strings.Contains(user, "Keep the original source bytes") || !strings.Contains(user, "Next review: Friday at noon") {
			t.Errorf("analysis provider did not receive the supplied meeting content")
		}
		return "The team agreed to retain source bytes and review the plan on Friday.", nil
	}}
	c := selectedEntryClient(t, ProviderApple, false, g)
	c.data = &testData{}
	c.localAI.askModel = meetingPasteAnswerModel{g}
	resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
	if err != nil || resolved.Action.Kind != intent.Ask || resolved.Action.Mode != "general" || g.calls != 0 {
		t.Fatalf("short analysis was not routed as a read: %+v calls=%d err=%v", resolved, g.calls, err)
	}
	answer, err := c.askMemoryWithMode(context.Background(), resolved.Action.Text, nil, resolved.Action.Mode)
	if err != nil || !answer.General || answer.Answer.Text != "The team agreed to retain source bytes and review the plan on Friday." || g.calls != 1 || answer.SentItems != 0 {
		t.Fatalf("short analysis failed through actual answer path: %+v calls=%d err=%v", answer, g.calls, err)
	}
}

func TestMeetingPasteRequiresDocumentStructureAndKeepsExplicitOperations(t *testing.T) {
	for _, input := range []string{
		"I need to call the dentist tomorrow",
		"Meeting agenda\nDiscuss the draft",
		"Some text\nAnother line\nOne more sentence",
		"Create tasks from these notes:\n" + meetingPasteFixture,
		"Remind me tomorrow at noon to review this:\n" + meetingPasteFixture,
		"Cancel task 1; supporting notes:\n" + meetingPasteFixture,
		"task: Review the transcript\n" + meetingPasteFixture,
		"Meeting\nAttendees: Maya, Niels\nDecision: keep it\x00",
	} {
		if got, ok := pastedNoteAction(input); ok {
			t.Errorf("non-note request or weak document evidence forced into paste route: %+v input=%q", got, input)
		}
	}
}

func TestMeetingPasteNegatedSaveRequestsClarification(t *testing.T) {
	g := &singleSearchModel{respond: func(context.Context, string, string) (string, error) { return `{"kind":"note"}`, nil }}
	c := selectedEntryClient(t, ProviderApple, false, g)
	input := "Do not save these notes yet:\n" + meetingPasteFixture
	resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
	if err != nil || resolved.Action.Kind != intent.Clarify || resolved.Message == "" || resolved.Action.When != "" || resolved.Target != nil || g.calls != 0 {
		t.Fatalf("negative save became a note or lost clarification: %+v calls=%d err=%v", resolved, g.calls, err)
	}
}

func TestMeetingPasteOverLimitSaveLeavesComposerIntact(t *testing.T) {
	input := "Meeting notes\n" + strings.Repeat("x", notes.MaxNoteBytes)
	adder := &noteAdder{}
	c := NewClient(&testData{}).WithNotes(adder)
	defer c.Close()
	app := &meetingPasteApp{&pausedAskApp{posted: make(chan func(), 8)}}
	d := &desktop{app: app, client: c, note: input, quickInput: input, quickNotePending: true}
	d.addNote()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for d.loading {
		select {
		case publish := <-app.posted:
			publish()
		case <-timeout.C:
			t.Fatal("oversize note save did not return to the composer")
		}
	}
	if d.quickInput != input || d.note != input || d.loading || d.status == "" || adder.text != "" {
		t.Fatalf("failed save removed composer bytes or reached persistence: composer=%d note=%d want=%d loading=%t status=%q saved=%d", len(d.quickInput), len(d.note), len(input), d.loading, d.status, len(adder.text))
	}
}
