package client

import (
	"context"
	"testing"

	"github.com/hazcod/blur/internal/intent"
)

func TestArithmeticEntryAnswersWithoutSavingOrRequiringModel(t *testing.T) {
	c := NewClient(&testData{})
	defer c.Close()
	for _, tc := range []struct{ input, want string }{
		{"421-12", "409"},
		{"(21 + 4) * 2", "50"},
		{"0.1 + 0.2", "0.3"},
		{"what is 100 / 4?", "25"},
		{"12 × 3", "36"},
	} {
		resolved, err := c.ResolveEntry(context.Background(), tc.input, EntryState{}, "UTC")
		if err != nil || resolved.Action.Kind != intent.Ask {
			t.Fatalf("calculation %q was routed to a write: %+v %v", tc.input, resolved.Action, err)
		}
		answer, err := c.AskMemory(context.Background(), resolved.Action.Text)
		if err != nil || answer.Answer.Text != tc.want || !answer.Direct || !answer.General || len(answer.Sources) != 0 || answer.SentItems != 0 {
			t.Fatalf("calculation %q needs AI or memory: %+v %v", tc.input, answer, err)
		}
	}
	resolved, err := c.ResolveEntry(context.Background(), "note: 421-12", EntryState{}, "UTC")
	if err != nil || resolved.Action.Kind != intent.Note || resolved.Action.Text != "421-12" {
		t.Fatalf("explicit numeric note lost write intent: %+v %v", resolved, err)
	}
}

func TestInvalidArithmeticIsNotSavedAsNote(t *testing.T) {
	c := NewClient(&testData{})
	defer c.Close()
	resolved, err := c.ResolveEntry(context.Background(), "12 / 0", EntryState{}, "UTC")
	if err != nil || resolved.Action.Kind != intent.Ask {
		t.Fatalf("division by zero was saved: %+v %v", resolved, err)
	}
	answer, err := c.AskMemory(context.Background(), "12 / 0")
	if err != nil || answer.Answer.Text != "Cannot divide by zero." || !answer.Direct {
		t.Fatalf("division error was not explained: %+v %v", answer, err)
	}
}
