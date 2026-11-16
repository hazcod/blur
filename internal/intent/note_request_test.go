package intent

import (
	"encoding/json"
	"testing"
)

func TestExplicitNoteRequestsPreserveTheOriginalBody(t *testing.T) {
	for _, test := range []struct{ input, text string }{
		{"note: Monday: get eggs", "Monday: get eggs"},
		{"Note to self: I need to review the PR", "I need to review the PR"},
		{"Save a note that The VAR_A  is nil.", "The VAR_A  is nil."},
		{"Could you jot this down: Aurora uses port 4317?", "Aurora uses port 4317?"},
		{"Please write this down: Monday: get eggs", "Monday: get eggs"},
		{"Can you take a note about the rollback owner?", "the rollback owner?"},
		{"Would you mind recording this: I bought eggs on Monday?", "I bought eggs on Monday?"},
		{"I'd like you to save this as a note: review the PR tomorrow", "review the PR tomorrow"},
		{"Jot down the access code is AbC-123.", "the access code is AbC-123."},
		{"Record this:\nMonday: get eggs\nTuesday: call Mira", "Monday: get eggs\nTuesday: call Mira"},
		{"Save these notes:\nMonday: get eggs\nTuesday: call Mira\n", "Monday: get eggs\nTuesday: call Mira\n"},
		{"Save these meeting notes: Aurora rollout\n  Mira owns rollback.\n", "Aurora rollout\n  Mira owns rollback.\n"},
		{"Could you save the following notes: PR review\nDecision: ship Friday.\n", "PR review\nDecision: ship Friday.\n"},
		{"Save a note: Do not send the release\n", "Do not send the release\n"},
		{"Save a note: Mira said \"don't send the release\".", "Mira said \"don't send the release\"."},
		{"note: About Face is a book", "About Face is a book"},
		{"note: that = 42", "that = 42"},
		{"note: Of Mice and Men", "Of Mice and Men"},
	} {
		t.Run(test.input, func(t *testing.T) {
			text, ok := NoteRequest(test.input)
			if !ok || text != test.text {
				t.Fatalf("explicit note lost its original content: %q, %t", text, ok)
			}
			wire, _ := json.Marshal(Action{Kind: Note, Text: test.text})
			got, err := Decode(string(wire), Request{Input: test.input})
			if err != nil || got.Kind != Note || got.Text != test.text {
				t.Fatalf("note request was rejected or changed: %+v, %v", got, err)
			}
		})
	}
}

func TestNoteAuthorityDoesNotComeFromQuestionsQuotedTextOrNegativeRequests(t *testing.T) {
	for _, input := range []string{
		"How do I save a note?", "What did I jot down yesterday?", "Don't write this down: the owner is Mira",
		"Could you not save a note that the port is 4317?", "Alice wrote: jot this down: buy eggs",
		"Meeting notes:\nAlice: save a note about the PR", "\"Save a note: buy eggs\"", "save a note", "jot this down",
	} {
		if text, ok := NoteRequest(input); ok {
			t.Errorf("non-authoritative request became a note: %q => %q", input, text)
		}
	}
}

func TestNegatedNoteOperationCannotBeAuthorizedByAModel(t *testing.T) {
	for _, input := range []string{"Do not save a note: Mira owns rollback", "Don't write this down: Mira owns rollback", "Could you not save a note that the port is 4317?", "Please don't save these notes:\nMonday: get eggs", "Never record this: private draft", "Do not save this: working thought", "Don't save this", "Don’t save this", "Never store this: working thought"} {
		wire, _ := json.Marshal(Action{Kind: Note, Text: input})
		if _, err := Decode(string(wire), Request{Input: input}); err != ErrActionAuthority {
			t.Errorf("negated note operation was accepted: %q err=%v", input, err)
		}
	}
}

func TestNoteRetentionDenialDoesNotConsumePositiveReminderOrLiteralBody(t *testing.T) {
	for _, input := range []string{"Don't forget to get eggs tomorrow", "Don't let me forget to collect my suit on Monday", "Do not let me forget to call Mira", "Note: Don't save this", "Save a note: Never store this draft"} {
		if NoteSavingDenied(input) {
			t.Errorf("positive reminder or literal note was treated as denied retention: %q", input)
		}
	}
}

func TestDirectRetentionDenialAppliesToAnyObjectAndLiteralNotesKeepIt(t *testing.T) {
	for _, prefix := range []string{"Do not ", "Don't ", "Don’t ", "Never ", "Please don't ", "Could you not "} {
		for _, verb := range []string{"save", "store", "remember", "note", "record", "capture", "keep", "retain"} {
			for _, object := range []string{"my password", "any of this", "the following: synthetic secret", "our conversation", "Mira’s sentence", ""} {
				for _, suffix := range []string{"", ".", "?"} {
					input := prefix + verb
					if object != "" {
						input += " " + object
					}
					input += suffix
					wire, _ := json.Marshal(Action{Kind: Note, Text: input})
					if !NoteSavingDenied(input) {
						t.Errorf("direct retention denial missed: %q", input)
					}
					if _, err := Decode(string(wire), Request{Input: input}); err != ErrActionAuthority {
						t.Errorf("model authorized denied retention: %q err=%v", input, err)
					}
					literal := "Note: " + input
					body, ok := NoteRequest(literal)
					if !ok || body != input || NoteSavingDenied(literal) {
						t.Errorf("literal note body changed or denied: %q => %q", literal, body)
					}
				}
			}
		}
	}
}
