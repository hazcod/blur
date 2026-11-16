package client

import (
	"context"
	"testing"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/intent"
)

func TestConditionalSavedTaskChangesClarifyBeforeBecomingExecutable(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	created, err := c.CreateTask(context.Background(), "Review PR", nil)
	if err != nil {
		t.Fatal(err)
	}
	epoch, _ := c.BeginMemoryRead()
	state := EntryState{Epoch: epoch, Targets: []assistant.TargetRef{{Kind: "task", ID: created.Item.ID, Revision: created.Item.Revision, Label: "T1"}}}
	for _, input := range []string{
		"cancel task 1 if Mira approves", "cancel task 1 unless Mira objects", "complete task 1 once the release ships",
		"complete task 1 when I get home", "cancel task 1 upon approval", "rename task 1 to New title if Mira approves",
		"move task 1 to Monday when Mira approves", "If Mira approves, cancel task 1", "I might cancel task 1",
		"I should rename task 1 to New title", "cancel task 1, but don't actually cancel it",
	} {
		t.Run(input, func(t *testing.T) {
			resolved, err := c.ResolveEntry(context.Background(), input, state, "UTC")
			if err != nil || resolved.Action.Kind != intent.Clarify || resolved.Target != nil || resolved.Draft != nil || resolved.Message == "" {
				t.Fatalf("conditional operation reached the executable action path: %+v, %v", resolved, err)
			}
		})
	}
}

func TestLiteralBoundTaskTitlesDoNotBecomeOperationConditions(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	created, err := c.CreateTask(context.Background(), "If I Stay", nil)
	if err != nil {
		t.Fatal(err)
	}
	epoch, _ := c.BeginMemoryRead()
	state := EntryState{Epoch: epoch, Targets: []assistant.TargetRef{{Kind: "task", ID: created.Item.ID, Revision: created.Item.Revision, Label: "T1"}}}
	resolved, err := c.ResolveEntry(context.Background(), "cancel the task called If I Stay", state, "UTC")
	if err != nil || resolved.Action.Kind != intent.TaskCancel || resolved.Target == nil {
		t.Fatalf("a literal task name lost explicit cancellation authority: %+v, %v", resolved, err)
	}
	resolved, err = c.ResolveEntry(context.Background(), "cancel the task called If I Stay if Mira approves", state, "UTC")
	if err != nil || resolved.Action.Kind != intent.Clarify || resolved.Target != nil {
		t.Fatalf("a trailing condition was swallowed by the literal task title: %+v, %v", resolved, err)
	}
}

func TestModelCannotCreateATaskFromANegatedOrReportedRequest(t *testing.T) {
	for _, input := range []string{"Do not create a task to buy milk", "Mira said: create a task to buy milk", "Create a task to buy milk if Mira approves"} {
		t.Run(input, func(t *testing.T) {
			g := &singleSearchModel{respond: func(context.Context, string, string) (string, error) {
				return `{"kind":"task","text":"buy milk"}`, nil
			}}
			c := selectedEntryClient(t, ProviderClaude, true, g)
			resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
			if err != nil || resolved.Action.Kind != intent.Clarify || resolved.Message == "" || resolved.Target != nil || resolved.Draft != nil || g.calls != 1 {
				t.Fatalf("non-authoritative model output became an action or opaque error: %+v calls=%d err=%v", resolved, g.calls, err)
			}
		})
	}
}

func TestExplicitNoteWithNegativeOrQuotedContentSkipsTheModel(t *testing.T) {
	g := &singleSearchModel{respond: func(context.Context, string, string) (string, error) {
		return `not valid intent JSON`, nil
	}}
	c := selectedEntryClient(t, ProviderClaude, true, g)
	for _, test := range []struct{ input, body string }{
		{"Save a note: Do not send the release\n", "Do not send the release\n"},
		{"Save a note: Mira said \"don't send the release\".\n", "Mira said \"don't send the release\".\n"},
	} {
		resolved, err := c.ResolveEntry(context.Background(), test.input, EntryState{}, "UTC")
		if err != nil || resolved.Action.Kind != intent.Note || resolved.Action.Text != test.body || resolved.Target != nil || resolved.Draft != nil || g.calls != 0 {
			t.Errorf("note body was confused with save authority: %+v calls=%d err=%v", resolved, g.calls, err)
		}
	}
}
