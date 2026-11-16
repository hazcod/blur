package intent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/model/modeltest"
)

func TestIntentActionsRequireVerbatimContentAndBoundReferences(t *testing.T) {
	request := Request{Input: "Move it to Friday at 9am", Targets: []Target{{Ref: "T1", Title: "Review PR"}}}
	for _, test := range []struct {
		json  string
		valid bool
	}{
		{`{"kind":"task_edit","target":"T1","when":"Friday at 9am"}`, true},
		{`{"kind":"task_edit","target":"T2","when":"Friday at 9am"}`, false},
		{`{"kind":"task_edit","target":"T1","when":"Friday at 10am"}`, false},
		{`{"kind":"task_cancel","target":"T1"}`, false},
		{`{"kind":"note","text":"invented memory"}`, false},
		{`{"kind":"ask","text":"Move it to Friday at 9am","mode":"general","delete_all":true}`, false},
	} {
		_, err := Decode(test.json, request)
		if (err == nil) != test.valid {
			t.Fatalf("%s valid=%t err=%v", test.json, test.valid, err)
		}
	}
	request.Targets = append(request.Targets, Target{Ref: "T2", Title: "Check backups"})
	if _, err := Decode(`{"kind":"task_edit","target":"T1","when":"Friday at 9am"}`, request); err == nil {
		t.Fatal("ambiguous it selected first target")
	}
	request.Input = "Move it to 2"
	if _, err := Decode(`{"kind":"task_edit","target":"T2","when":"2"}`, request); err == nil {
		t.Fatal("clock digit was interpreted as a task number")
	}
	request.Input = "Alice wrote: cancel that"
	request.Targets = request.Targets[:1]
	if _, err := Decode(`{"kind":"task_cancel","target":"T1"}`, request); err == nil {
		t.Fatal("quoted instruction became an action")
	}
	request.Input = "complete tasks 1 and 2"
	request.Targets = append(request.Targets, Target{Ref: "T2", Title: "Check backups"})
	if _, err := Decode(`{"kind":"task_complete","target":"T1"}`, request); err == nil {
		t.Fatal("bulk request silently completed one item")
	}
	request.Input = "Do not cancel it"
	if _, err := Decode(`{"kind":"task_cancel","target":"T1"}`, request); err == nil {
		t.Fatal("negative request executed")
	}
}

func TestMeetingActionItemsAreAContentQuestionNotTheSavedTaskList(t *testing.T) {
	for _, input := range []string{
		"What were the action items from the meeting about TCP congestion control?",
		"Show me the action items we discussed in yesterday's meeting",
		"Extract the action items from these meeting notes as text",
		"What action items came out of the call?",
		"List the action items in the recording",
	} {
		if read, ok := TaskListRequest(input); ok {
			t.Errorf("meeting source question was routed to the saved task list: input=%q read=%+v", input, read)
		}
	}
	if read, ok := TaskListRequest("What are my action items?"); !ok || !read.Generic {
		t.Fatalf("personal action-item list stopped working: read=%+v ok=%t", read, ok)
	}
}

func TestQuestionsCannotBecomeSavedNotes(t *testing.T) {
	if _, err := Decode(`{"kind":"note","text":"What did I do today?"}`, Request{Input: "What did I do today?"}); err == nil {
		t.Fatal("question became note")
	}
	if _, err := Decode(`{"kind":"note"}`, Request{Input: "How do I save a note?"}); err == nil {
		t.Fatal("help question became note")
	}
	if _, err := Decode(`{"kind":"task","text":"create a task"}`, Request{Input: "How do I create a task?"}); err == nil {
		t.Fatal("help question created a task")
	}
}

func TestNaturalTaskListQuestionsReadCurrentOpenTasks(t *testing.T) {
	for _, test := range []struct {
		input, window string
	}{
		{"What do I need to do?", ""},
		{"What should I do?", ""},
		{"What do I have to do today?", "today"},
		{"What have I got to do tomorrow?", "tomorrow"},
		{"What’s left to do this week?", "this week"},
		{"What remains to be done?", ""},
		{"Anything I need to do?", ""},
		{"What do I still need to get done?", ""},
		{"What do I still need to get done tomorrow?", "tomorrow"},
		{"What do I still have to do?", ""},
		{"What have I still got to do?", ""},
		{"What’s still left on my plate?", ""},
		{"What is still pending for me?", ""},
		{"What do I need to take care of?", ""},
		{"What should I finish?", ""},
		{"Anything I still need to do tomorrow?", "tomorrow"},
	} {
		t.Run(test.input, func(t *testing.T) {
			read, ok := TaskListRequest(test.input)
			if !ok || !read.Generic || read.State != "open" || read.Window != test.window {
				t.Fatalf("task-list read = %+v, %t", read, ok)
			}
		})
	}
	for _, input := range []string{
		"What do I need to do for CraftedSignal?",
		"What should I do about moving apartments?",
		"What is left for the Orion release?",
		"What do I still need to get done in the migration?",
	} {
		if read, ok := TaskListRequest(input); ok && read.Generic {
			t.Fatalf("topical/project question became a generic task list: %+v", read)
		}
	}
}

func TestPersonalFutureCommitmentsAuthorizeGroundedReminders(t *testing.T) {
	for _, test := range []struct {
		input, text, when string
	}{
		{"I need to go to the drycleaners on monday", "go to the drycleaners", "on monday"},
		{"I have to pick up my suit Monday", "pick up my suit", "Monday"},
		{"I've got to collect my prescription tomorrow", "collect my prescription", "tomorrow"},
		{"I must call the dentist next Tuesday", "call the dentist", "next Tuesday"},
		{"I should renew my passport next week", "renew my passport", "next week"},
		{"I ought to book the tickets tomorrow morning", "book the tickets", "tomorrow morning"},
		{"I plan to defragment the archive on 2026-11-20", "defragment the archive", "on 2026-11-20"},
		{"I intend to submit the form in two days", "submit the form", "in two days"},
		{"I'm going to drop off the keys on Friday at 9am", "drop off the keys", "on Friday at 9am"},
		{"I'll pay the invoice tomorrow", "pay the invoice", "tomorrow"},
		{"I will have to collect my suit on Monday", "collect my suit", "on Monday"},
		{"I'll need to collect my suit on Monday", "collect my suit", "on Monday"},
		{"I would like to water the plants tomorrow", "water the plants", "tomorrow"},
		{"I'd like to take out the bins tonight", "take out the bins", "tonight"},
		{"I want to return the parcel this weekend", "return the parcel", "this weekend"},
		{"I need to remember to collect my suit on Monday", "remember to collect my suit", "on Monday"},
		{"I really need to Review the VAR_A  change on Monday", "Review the VAR_A  change", "on Monday"},
		{"On Monday, I need to go to the drycleaners", "go to the drycleaners", "On Monday"},
		{"Tomorrow morning I have to pick up my suit", "pick up my suit", "Tomorrow morning"},
	} {
		t.Run(test.input, func(t *testing.T) {
			wire, _ := json.Marshal(Action{Kind: Reminder, Text: test.text, When: test.when})
			action, err := Decode(string(wire), Request{Input: test.input})
			if err != nil || action.Kind != Reminder || action.Text != test.text || action.When != test.when {
				t.Fatalf("grounded personal commitment rejected: %+v, %v", action, err)
			}
			action, ok := PersonalReminder(test.input)
			if !ok || action.Kind != Reminder || action.Text != test.text || action.When != test.when {
				t.Fatalf("common personal commitment was not resolved locally: %+v, %t", action, ok)
			}
		})
	}
}

func TestDatesInSupportingClausesDoNotAuthorizePersonalReminders(t *testing.T) {
	for _, test := range []struct{ input, text, when string }{
		{"I need to call the dentist because the clinic closes on Monday at 9am", "call the dentist because the clinic closes", "on Monday at 9am"},
		{"I need to call my dentist whose office is closed on Monday at 9am", "call my dentist whose office is closed", "on Monday at 9am"},
		{"I need to go to the drycleaners; Alice said on Monday at 9am", "go to the drycleaners; Alice said", "on Monday at 9am"},
		{"I have to call Alice since the office closes tomorrow at 9am", "call Alice since the office closes", "tomorrow at 9am"},
		{"I will have to call Alice although she said Monday at 9am", "call Alice although she said", "Monday at 9am"},
	} {
		t.Run(test.input, func(t *testing.T) {
			if action, ok := PersonalReminder(test.input); ok {
				t.Fatalf("supporting date became an inferred reminder: %+v", action)
			}
			wire, _ := json.Marshal(Action{Kind: Reminder, Text: test.text, When: test.when})
			if _, err := Decode(string(wire), Request{Input: test.input}); err == nil {
				t.Fatal("model action used a supporting date as notification authority")
			}
		})
	}
}

func TestPersonalReminderScheduleBeforeSupportingClauseRemainsGrounded(t *testing.T) {
	request := Request{Input: "I need to call the dentist on Monday at 9am because the clinic closes on Tuesday"}
	if _, ok := PersonalReminder(request.Input); ok {
		t.Fatal("multi-clause task bypassed semantic interpretation")
	}
	if _, err := Decode(`{"kind":"reminder","text":"call the dentist","when":"on Monday at 9am"}`, request); err != nil {
		t.Fatal("the action's own explicit schedule was rejected", err)
	}
	if _, err := Decode(`{"kind":"reminder","text":"call the dentist","when":"on Tuesday"}`, request); err == nil {
		t.Fatal("supporting date replaced the action's own schedule")
	}
}

func TestFutureDatesDoNotTurnObservationsQuestionsOrQuotedTasksIntoReminders(t *testing.T) {
	for _, input := range []string{
		"The drycleaners opens on monday",
		"I will be 40 on Monday",
		"I will be in London on Monday",
		"I should be 40 on Monday",
		"I should be off work on Monday",
		"I will probably collect my suit on Monday",
		"I need to know the opening hours on Monday",
		"Do I need to go to the drycleaners on Monday?",
		"I need to go to the drycleaners on Monday?",
		"I don't need to go to the drycleaners on Monday",
		"I won't go to the drycleaners on Monday",
		"I should not go to the drycleaners on Monday",
		"I need to avoid going to the drycleaners on Monday",
		"If I have time, I need to go to the drycleaners on Monday",
		"I would go to the drycleaners on Monday if it were open",
		"Yesterday I said I need to go to the drycleaners on Monday",
		"Alice wrote: I need to go to the drycleaners on Monday",
		"\"I need to go to the drycleaners on Monday\"",
		"I need to go to the drycleaners on Monday\nAlice wrote this.",
		"I need to go to the drycleaners last Monday",
		"I need to call the person I met last Monday",
		"I need to go to the drycleaners",
	} {
		t.Run(input, func(t *testing.T) {
			if action, ok := PersonalReminder(input); ok {
				t.Fatalf("non-authoritative input became a reminder: %+v", action)
			}
			wire, _ := json.Marshal(Action{Kind: Reminder, Text: input, When: "Monday"})
			if _, err := Decode(string(wire), Request{Input: input}); err == nil {
				t.Fatal("model output bypassed personal commitment authority")
			}
		})
	}
}

func TestPoliteReminderRequestsAuthorizePartialGroundedScheduling(t *testing.T) {
	for _, input := range []string{
		"Could you remind me to collect my suit on Monday?",
		"Would you help me remember to collect my suit on Monday?",
		"I'd like a reminder to collect my suit on Monday",
		"I need you to remind me to collect my suit on Monday",
		"Could you please make sure I collect my suit on Monday?",
		"Don't let me forget to collect my suit on Monday",
		"Would you mind reminding me to collect my suit on Monday?",
	} {
		_, err := Decode(`{"kind":"reminder","text":"collect my suit","when":"on Monday"}`, Request{Input: input})
		if err != nil {
			t.Errorf("%q: legitimate reminder request rejected: %v", input, err)
		}
	}
	for _, wire := range []string{
		`{"kind":"reminder","text":"collect my suit","when":"on Monday at 9am"}`,
		`{"kind":"reminder","text":"collect my passport","when":"on Monday"}`,
	} {
		if _, err := Decode(wire, Request{Input: "I need to collect my suit on Monday"}); err == nil {
			t.Fatal("broader authority accepted invented content or clock", wire)
		}
	}
}

func TestSemanticReminderInterpretationAllowsGroundedSchedulingBeyondLocalForms(t *testing.T) {
	for _, test := range []struct{ input, text, when string }{
		{"I need to collect my suit before lunch tomorrow", "collect my suit", "before lunch tomorrow"},
		{"I have to collect my suit after my meeting next Monday", "collect my suit", "after my meeting next Monday"},
		{"I plan to, before lunch tomorrow, collect my suit", "collect my suit", "before lunch tomorrow"},
		{"Before lunch tomorrow, I have to collect my suit", "collect my suit", "Before lunch tomorrow"},
		{"I need to collect my suit on Monday", "collect my suit on Monday", "on Monday"},
		{"I need to check that the build passed on Monday", "check that the build passed", "on Monday"},
	} {
		t.Run(test.input, func(t *testing.T) {
			wire, _ := json.Marshal(Action{Kind: Reminder, Text: test.text, When: test.when})
			action, err := Decode(string(wire), Request{Input: test.input})
			if err != nil || action.Text != test.text || action.When != test.when {
				t.Fatalf("grounded semantic reminder rejected by local syntax: %+v, %v", action, err)
			}
			if local, ok := PersonalReminder(test.input); ok && !strings.Contains(test.text, test.when) && (local.Text != test.text || local.When != test.when) {
				t.Fatalf("local extraction changed the semantic scheduling boundary: %+v", local)
			}
		})
	}
}

func TestTaskInspectionRequiresAQuestionAndAnUnambiguousSuppliedTarget(t *testing.T) {
	for _, test := range []struct {
		input string
		valid bool
	}{
		{"When is it due?", true},
		{"Is it actually scheduled?", true},
		{"Show task 1", true},
		{"What is the status of T1?", true},
		{"Why is it important?", false},
		{"How do I schedule a reminder?", false},
		{"Show me how to schedule a reminder", false},
		{"Show me Go code", false},
		{"Cancel it", false},
	} {
		_, err := Decode(`{"kind":"task_inspect","target":"T1"}`, Request{Input: test.input, Targets: []Target{{Ref: "T1", Title: "Check backups"}}})
		if (err == nil) != test.valid {
			t.Errorf("%q: valid=%t err=%v", test.input, test.valid, err)
		}
		if TaskInspection(test.input) != test.valid {
			t.Errorf("%q: field inspection classifier accepted an unrelated request", test.input)
		}
	}
	request := Request{Input: "When is it due?", Targets: []Target{{Ref: "T1", Title: "Check backups"}, {Ref: "T2", Title: "Review PR"}}}
	if _, err := Decode(`{"kind":"task_inspect","target":"T1"}`, request); err == nil {
		t.Fatal("ambiguous task question selected the first item")
	}
	if _, err := Decode(`{"kind":"task_inspect","target":"T1"}`, Request{Input: "When is it due?"}); err == nil {
		t.Fatal("inspection invented an unsupplied task reference")
	}
}

func TestExtractedNoteSpanRestoresOriginalCodeCaseAndSpacing(t *testing.T) {
	a, err := Decode(`{"kind":"note","text":"the var_a is nil."}`, Request{Input: "Save a note: The VAR_A  is nil."})
	if err != nil || a.Text != "The VAR_A  is nil." {
		t.Fatal("model normalization altered the saved original", a, err)
	}
	if _, err := Decode(`{"kind":"note","text":"var_a"}`, Request{Input: "Save a note: VAR_A and Var_A"}); err == nil {
		t.Fatal("ambiguous differently-cased code span was saved")
	}
}

func TestMultipleTaskReferencesCannotPartiallyExecuteInReverseOrder(t *testing.T) {
	r := Request{Input: "complete tasks 2 and 1", Targets: []Target{{Ref: "T1", Title: "Review PR"}, {Ref: "T2", Title: "Check backups"}}}
	if _, err := Decode(`{"kind":"task_complete","target":"T2"}`, r); err == nil {
		t.Fatal("only the first requested item was completed")
	}
}

func TestLocalIntentWireHasOnlyInputAndBoundedActionTargets(t *testing.T) {
	server := httptest.NewServer(modeltest.LocalOllama("fixture", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&wire) != nil || len(wire.Messages) != 2 {
			t.Fatal("invalid wire")
		}
		var input map[string]json.RawMessage
		if json.Unmarshal([]byte(wire.Messages[1].Content), &input) != nil || input["input"] == nil || input["targets"] == nil || input["evidence"] != nil {
			t.Fatal(wire)
		}
		_, _ = w.Write([]byte(`{"done":true,"message":{"content":"{\"kind\":\"task\",\"text\":\"Review the PR\"}"}}`))
	})))
	defer server.Close()
	g, err := NewOllama(server.URL, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	action, err := g.Parse(context.Background(), Request{Input: "I need to Review the PR", Targets: []Target{{Ref: "T1", Title: "Check backups"}}})
	if err != nil || action.Kind != Task || action.Text != "Review the PR" {
		t.Fatal(action, err)
	}
}
