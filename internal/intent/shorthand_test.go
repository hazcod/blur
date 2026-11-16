package intent

import (
	"encoding/json"
	"testing"
)

func TestScheduledTaskShorthandKeepsVerbatimTitleAndTiming(t *testing.T) {
	for _, test := range []struct{ input, text, when string }{
		{"monday: get eggs", "get eggs", "monday"},
		{"Monday:get eggs", "get eggs", "Monday"},
		{"Tomorrow, buy milk", "buy milk", "Tomorrow"},
		{"tomorrow get eggs", "get eggs", "tomorrow"},
		{"on Monday buy eggs", "buy eggs", "on Monday"},
		{"Monday at 9am: collect my suit", "collect my suit", "Monday at 9am"},
		{"2026-11-20: Review the VAR_A  change", "Review the VAR_A  change", "2026-11-20"},
		{"get eggs on Monday", "get eggs", "on Monday"},
		{"Please pick up the drycleaning tomorrow", "pick up the drycleaning", "tomorrow"},
		{"Drycleanres on monday", "Drycleanres", "on monday"},
		{"Monday: dentist appointment", "dentist appointment", "Monday"},
		{"in two days: deploy Aurora", "deploy Aurora", "in two days"},
		{"on Monday I need to buy eggs", "buy eggs", "on Monday"},
		{"Mon.: get eggs", "get eggs", "Mon."},
		{"this Sunday: get eggs", "get eggs", "this Sunday"},
		{"day after tomorrow get eggs", "get eggs", "day after tomorrow"},
		{"get eggs on November 20th, 2026", "get eggs", "on November 20th, 2026"},
		{"on 20 Nov: get eggs", "get eggs", "on 20 Nov"},
		{"tomorrow 9am: get eggs", "get eggs", "tomorrow 9am"},
		{"Fri. at 6 p.m.: get eggs", "get eggs", "Fri. at 6 p.m."},
	} {
		t.Run(test.input, func(t *testing.T) {
			action, ok := PersonalReminder(test.input)
			if !ok || action.Kind != Reminder || action.Text != test.text || action.When != test.when {
				t.Fatalf("scheduled shorthand was not understood: %+v, %t", action, ok)
			}
			wire, _ := json.Marshal(Action{Kind: Reminder, Text: test.text, When: test.when})
			action, err := Decode(string(wire), Request{Input: test.input})
			if err != nil || action.Text != test.text || action.When != test.when {
				t.Fatalf("grounded model shorthand was rejected: %+v, %v", action, err)
			}
		})
	}
}

func TestScheduledShorthandDoesNotAuthorizeObservationsOrEmbeddedInstructions(t *testing.T) {
	for _, input := range []string{
		"Monday: the shop is closed",
		"Monday the shop closes",
		"Monday: I bought eggs",
		"Monday: Alice said get eggs",
		"Monday: what should I buy",
		"Monday: get eggs?",
		"Monday: don't get eggs",
		"Monday: maybe get eggs",
		"Monday: get eggs if the shop is open",
		"Monday: 421-12",
		"Monday: note: get eggs",
		"Get eggs last Monday",
		"I got eggs on Monday",
		"The drycleaners opens on Monday",
		"Alice wrote: Monday: get eggs",
		"\"Monday: get eggs\"",
		"Monday: get eggs\nTuesday: buy milk",
	} {
		t.Run(input, func(t *testing.T) {
			if action, ok := PersonalReminder(input); ok {
				t.Fatalf("non-task input became a scheduled action: %+v", action)
			}
			wire, _ := json.Marshal(Action{Kind: Reminder, Text: input, When: "Monday"})
			if _, err := Decode(string(wire), Request{Input: input}); err == nil {
				t.Fatal("model bypassed shorthand authority")
			}
		})
	}
}

func TestShorthandModelCannotChangeTaskOrInventTiming(t *testing.T) {
	request := Request{Input: "Monday: get eggs"}
	for _, wire := range []string{
		`{"kind":"reminder","text":"get milk","when":"Monday"}`,
		`{"kind":"reminder","text":"get eggs","when":"Monday at 9am"}`,
		`{"kind":"reminder","text":"Monday","when":"Monday"}`,
	} {
		if _, err := Decode(wire, request); err == nil {
			t.Fatalf("unrelated or invented shorthand accepted: %s", wire)
		}
	}
}

func TestUndatedPersonalTaskKeepsItsActionWithoutInferringADate(t *testing.T) {
	for _, test := range []struct{ input, text string }{
		{"I need to review the PR", "review the PR"},
		{"I've got to collect my prescription", "collect my prescription"},
		{"I should defragment the archive", "defragment the archive"},
		{"I really need to Review the VAR_A  change", "Review the VAR_A  change"},
	} {
		t.Run(test.input, func(t *testing.T) {
			action, ok := PersonalTask(test.input)
			if !ok || action.Kind != Task || action.Text != test.text || action.When != "" {
				t.Fatalf("undated commitment became a note or invented timing: %+v, %t", action, ok)
			}
		})
	}
	for _, input := range []string{
		"I need to review the PR tomorrow", "I need to check backups before lunch", "I need to collect my suit in two days",
		"I need to know the opening hours", "I should be off work", "I need to avoid going there", "I need to go there?",
		"I don't need to review the PR", "Maybe I need to review the PR", "I need to review the PR because Alice said so",
	} {
		if action, ok := PersonalTask(input); ok {
			t.Errorf("uncertain or dated input became a plain task: %q => %+v", input, action)
		}
	}
}

func TestModelMayInterpretUnfamiliarScheduledActionsWithoutALexicalAllowlist(t *testing.T) {
	for _, test := range []struct{ input, text, when string }{
		{"reconcile ingress topology next Monday", "reconcile ingress topology", "next Monday"},
		{"Tomorrow: spline-fit the pressure data", "spline-fit the pressure data", "Tomorrow"},
		{"triage latency regressions on Friday", "triage latency regressions", "on Friday"},
		{"ask Mira who owns the rollback on Monday", "ask Mira who owns the rollback", "on Monday"},
		{"I need to compare the two crash reports on Monday", "compare the two crash reports", "on Monday"},
	} {
		wire, _ := json.Marshal(Action{Kind: Reminder, Text: test.text, When: test.when})
		action, err := Decode(string(wire), Request{Input: test.input})
		if err != nil || action.Text != test.text || action.When != test.when {
			t.Errorf("semantic classification was blocked by local vocabulary: %q => %+v, %v", test.input, action, err)
		}
	}
	for _, test := range []struct{ input, text string }{
		{"Mira owns the rollback on Monday", "Mira owns the rollback"},
		{"Monday: Noor prefers tea without sugar", "Noor prefers tea without sugar"},
		{"Monday: Safari exposes the page title through Accessibility", "Safari exposes the page title through Accessibility"},
		{"Monday: I bought eggs", "I bought eggs"},
	} {
		wire, _ := json.Marshal(Action{Kind: Reminder, Text: test.text, When: "Monday"})
		if _, err := Decode(string(wire), Request{Input: test.input}); err == nil {
			t.Errorf("declarative observation authorized a new reminder: %q", test.input)
		}
	}
	// Extracting a weekday without its optional "on" is still an exact,
	// equivalent scheduling span; it must not lose meaningful this/next words.
	if _, err := Decode(`{"kind":"reminder","text":"triage latency regressions","when":"Friday"}`, Request{Input: "triage latency regressions on Friday"}); err != nil {
		t.Errorf("equivalent grounded preposition-free date was rejected: %v", err)
	}
	if _, err := Decode(`{"kind":"reminder","text":"reconcile ingress topology","when":"Monday"}`, Request{Input: "reconcile ingress topology this Monday"}); err == nil {
		t.Error("meaningful this/next date qualifier was discarded")
	}
}

func TestFutureTimingDoesNotAuthorizeImmediateTaskCompletionOrCancellation(t *testing.T) {
	for _, test := range []struct {
		input string
		kind  Kind
	}{
		{"Complete task 1 tomorrow", TaskComplete},
		{"Cancel task 1 on Monday", TaskCancel},
		{"Finish it after lunch", TaskComplete},
	} {
		wire, _ := json.Marshal(Action{Kind: test.kind, Target: "T1"})
		if _, err := Decode(string(wire), Request{Input: test.input, Targets: []Target{{Ref: "T1", Title: "Review PR"}}}); err == nil {
			t.Errorf("future operation was accepted for immediate execution: %q", test.input)
		}
	}
}

func TestUnpunctuatedInformationQuestionsCannotBecomeSavedNotes(t *testing.T) {
	for _, input := range []string{
		"Is Mira the rollback owner", "Are the backups encrypted", "Does Aurora use port 4317", "Was the migration finished",
		"Has the release shipped", "Have we discussed the incident", "Can I use SQLite for this", "Should I rotate the certificate",
	} {
		if !QuestionPrefix(input) {
			t.Errorf("clear information question was not recognized: %q", input)
		}
		if _, err := Decode(`{"kind":"note"}`, Request{Input: input}); err == nil {
			t.Errorf("unpunctuated question was accepted as a note: %q", input)
		}
	}
}
