package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/model"
)

func TestQuestionShorthandRoutesReadOnlyWithoutIntentGeneration(t *testing.T) {
	for _, input := range []string{"laundry?", "Compiler examples？", "昨日のメモは？", "هل لدي مهام؟", "rollback owner?!", `"Who owns rollback?"`, "“Mira?”"} {
		t.Run(input, func(t *testing.T) {
			g := &singleSearchModel{respond: func(context.Context, string, string) (string, error) {
				return "malformed synthetic interpretation", nil
			}}
			c := selectedEntryClient(t, ProviderApple, false, g)
			resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
			if err != nil || resolved.Action.Kind != intent.Ask || resolved.Action.Text != input || g.calls != 0 {
				t.Fatalf("read question depended on intent model or became a write: %+v calls=%d err=%v", resolved, g.calls, err)
			}
		})
	}
}

func TestNumericRequestsPrecedeCodeClarificationAndKeepExplicitNotes(t *testing.T) {
	for _, input := range []string{"421-12", "(8+4)/3", "0.1 + 0.2", "2^10", "sqrt(4)", "pow(2, 10)", "sin(0)"} {
		g := &singleSearchModel{}
		c := selectedEntryClient(t, ProviderApple, false, g)
		resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
		if err != nil || resolved.Action.Kind != intent.Ask || resolved.Action.Mode != "general" || resolved.Action.Text != input || g.calls != 0 {
			t.Fatalf("numeric request became code or waited for intent: %+v calls=%d err=%v", resolved, g.calls, err)
		}
		resolved, err = c.ResolveEntry(context.Background(), "note: "+input, EntryState{}, "UTC")
		if err != nil || resolved.Action.Kind != intent.Note || resolved.Action.Text != input {
			t.Fatal("explicit calculation note lost authority", resolved, err)
		}
	}
}

func TestStructuredInputClarifiesWithoutWritesOrIntentGeneration(t *testing.T) {
	for _, input := range []string{
		"https://example.org/page?text=remind+me+tomorrow", "```sh\necho 'remind me tomorrow'\n```",
		`{"message":"cancel task 1","when":"tomorrow"}`, `["remind me tomorrow", "buy eggs"]`,
		"$ echo 'Monday: get eggs'", "count := 12", `fmt.Println("remind me tomorrow")`,
		`"cancel task 1"`, "“monday: get eggs”", "…?!", "🙂", "bad\x00text", string([]byte{0xff}),
		"func main() { println(42) }", "def greet(name):\n    print(name)", "SELECT name FROM users;", "name,role\nMira,owner",
	} {
		t.Run(input, func(t *testing.T) {
			for _, selected := range []bool{false, true} {
				g := &singleSearchModel{respond: func(context.Context, string, string) (string, error) { return `{"kind":"note"}`, nil }}
				c := selectedEntryClient(t, ProviderApple, false, g)
				if !selected {
					c.localAI.settings.Enabled = false
				}
				resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
				if err != nil || resolved.Action.Kind != intent.Clarify || resolved.Message == "" || resolved.Draft != nil || resolved.Target != nil || g.calls != 0 {
					t.Fatalf("structured data became actionable or reached inference: %+v calls=%d err=%v", resolved, g.calls, err)
				}
			}
		})
	}
}

func TestInvalidInterpretationOffersSafeClarificationAndPreservesProviderErrors(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		err            error
		wantClarify    bool
	}{
		{"malformed", "not JSON", nil, true},
		{"invented task", `{"kind":"task","text":"invented unrelated action"}`, nil, true},
		{"provider unavailable", "", model.ErrAppleUnavailable, false},
		{"provider budget", "", model.ErrAppleBudget, false},
		{"canceled", "", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &singleSearchModel{respond: func(context.Context, string, string) (string, error) { return tc.response, tc.err }}
			c := selectedEntryClient(t, ProviderClaude, true, g)
			resolved, err := c.ResolveEntry(context.Background(), "Compiler examples", EntryState{}, "UTC")
			if tc.wantClarify {
				if err != nil || resolved.Action.Kind != intent.Clarify || !strings.Contains(resolved.Message, "note") || resolved.Draft != nil || resolved.Target != nil {
					t.Fatalf("invalid interpretation was brittle or actionable: %+v %v", resolved, err)
				}
			} else if !errors.Is(err, tc.err) {
				t.Fatalf("provider/cancel failure hidden: %v want %v", err, tc.err)
			}
		})
	}
}

func TestEmptyEntryPrefixesAskForTheirMissingContentLocally(t *testing.T) {
	for _, input := range []string{"ask:", "note:", "task:", "todo:", "reminder:", "remember:", "preference:", "rule:", "writing rule:", "please note:  "} {
		t.Run(input, func(t *testing.T) {
			g := &singleSearchModel{}
			c := selectedEntryClient(t, ProviderApple, false, g)
			resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
			if err != nil || resolved.Action.Kind != intent.Clarify || resolved.Message == "" || g.calls != 0 {
				t.Fatalf("empty operation was silent or tried inference: %+v calls=%d err=%v", resolved, g.calls, err)
			}
		})
	}
}

func TestExplicitNotesRetainStructuredContentAndQuestionsDoNotGrantWrites(t *testing.T) {
	for _, body := range []string{"https://example.org/", "```sh\necho hello\n```", `{"command":"cancel task 1"}`, "🙂", "count := 12", "func main() { println(42) }", "SELECT name FROM users;", "name,role\nMira,owner"} {
		g := &singleSearchModel{}
		c := selectedEntryClient(t, ProviderApple, false, g)
		resolved, err := c.ResolveEntry(context.Background(), "note: "+body, EntryState{}, "UTC")
		if err != nil || resolved.Action.Kind != intent.Note || resolved.Action.Text != body || g.calls != 0 {
			t.Fatalf("explicit note lost original data: %+v calls=%d err=%v", resolved, g.calls, err)
		}
	}
	for _, input := range []string{"Could you schedule a reminder for Friday at nine am to check backups?", "Can you save the important parts of my last conversation?", "Please add reviewing the PR to my tasks"} {
		g := &singleSearchModel{respond: func(context.Context, string, string) (string, error) { return `{"kind":"clarify"}`, nil }}
		c := selectedEntryClient(t, ProviderApple, false, g)
		resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
		if err != nil || resolved.Action.Kind != intent.Clarify || g.calls == 0 {
			t.Fatalf("polite write question was treated as a deterministic read: %+v calls=%d err=%v", resolved, g.calls, err)
		}
	}
}

func TestCodeDataClarificationDoesNotInterceptNormalPersonalProse(t *testing.T) {
	for _, input := range []string{"Mira owns rollback.", "Class diagrams help the team", "Select the rollout owner from the attendees", "Mira, owner of rollback\nJonas, handles docs", "We use func main() in the sample"} {
		if message := entryDataClarification(input); message != "" {
			t.Fatalf("ordinary prose was intercepted: input=%q message=%q", input, message)
		}
	}
}

func TestNegatedNoteEntryClarifiesBeforeModelAndPositiveNoteKeepsNegatedData(t *testing.T) {
	for _, input := range []string{"Do not save a note: Mira owns rollback", "Don't write this down: Mira owns rollback", "Could you not save a note that the port is 4317?", "Never record this: draft", "Do not save this: working thought", "Don't save this", "Don’t save this", "Never store this: working thought"} {
		g := &singleSearchModel{respond: func(context.Context, string, string) (string, error) { return `{"kind":"note"}`, nil }}
		c := selectedEntryClient(t, ProviderApple, false, g)
		resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
		if err != nil || resolved.Action.Kind != intent.Clarify || resolved.Message == "" || g.calls != 0 {
			t.Fatalf("negated note operation reached write/model path: %+v calls=%d err=%v", resolved, g.calls, err)
		}
	}
	g := &singleSearchModel{}
	c := selectedEntryClient(t, ProviderApple, false, g)
	resolved, err := c.ResolveEntry(context.Background(), "Save a note: Do not send the release\n", EntryState{}, "UTC")
	if err != nil || resolved.Action.Kind != intent.Note || resolved.Action.Text != "Do not send the release\n" || g.calls != 0 {
		t.Fatal("note data was mistaken for denied operation", resolved, err)
	}
}

func TestDirectRetentionDenialsNeverBecomeNotesWithOrWithoutSelectedModel(t *testing.T) {
	for _, selected := range []bool{false, true} {
		g := &singleSearchModel{respond: func(context.Context, string, string) (string, error) { return `{"kind":"note"}`, nil }}
		c := selectedEntryClient(t, ProviderApple, false, g)
		c.localAI.settings.Enabled = selected
		for _, prefix := range []string{"Do not ", "Don't ", "Don’t ", "Never ", "Please don't ", "Could you not "} {
			for _, verb := range []string{"save", "store", "remember", "note", "record", "capture", "keep", "retain"} {
				for _, object := range []string{"my password", "any of this", "the following: synthetic secret", "our conversation"} {
					for _, suffix := range []string{"", "?"} {
						input := prefix + verb + " " + object + suffix
						got, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
						if err != nil || got.Action.Kind != intent.Clarify || got.Message == "" || g.calls != 0 {
							t.Fatalf("denied retention became write or inference: selected=%t input=%q got=%+v calls=%d err=%v", selected, input, got, g.calls, err)
						}
					}
				}
			}
		}
	}
}
