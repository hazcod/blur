package intent

import (
	"encoding/json"
	"testing"
)

func TestSavedTaskMutationsRequireCurrentUnconditionalAuthority(t *testing.T) {
	for _, test := range []struct {
		input string
		kind  Kind
		text  string
		when  string
	}{
		{"cancel task 1 if Mira approves", TaskCancel, "", ""},
		{"cancel task 1 unless Mira objects", TaskCancel, "", ""},
		{"complete task 1 once the release ships", TaskComplete, "", ""},
		{"complete task 1 when I get home", TaskComplete, "", ""},
		{"cancel task 1 upon approval", TaskCancel, "", ""},
		{"cancel task 1 pending approval", TaskCancel, "", ""},
		{"cancel task 1 whenever Mira approves", TaskCancel, "", ""},
		{"cancel task 1 subject to approval", TaskCancel, "", ""},
		{"cancel task 1 not now", TaskCancel, "", ""},
		{"rename task 1 to New title if Mira approves", TaskEdit, "New title if Mira approves", ""},
		{"move task 1 to Monday when Mira approves", TaskEdit, "", "Monday when Mira approves"},
		{"If Mira approves, cancel task 1", TaskCancel, "", ""},
		{"I might cancel task 1", TaskCancel, "", ""},
		{"I should rename task 1 to New title", TaskEdit, "New title", ""},
		{"cancel task 1, but don't actually cancel it", TaskCancel, "", ""},
		{"rename task 1 to \"New title\" if Mira approves", TaskEdit, "New title", ""},
	} {
		t.Run(test.input, func(t *testing.T) {
			wire, _ := json.Marshal(Action{Kind: test.kind, Target: "T1", Text: test.text, When: test.when})
			if got, err := Decode(string(wire), Request{Input: test.input, Targets: []Target{{Ref: "T1", Title: "Review PR"}}}); err == nil {
				t.Fatalf("conditional/modal mutation became executable: %+v", got)
			}
		})
	}
}

func TestMutationAuthorityKeepsBoundTitleDataSeparateFromConditions(t *testing.T) {
	for _, test := range []struct {
		input, title string
		kind         Kind
		text         string
		valid        bool
	}{
		{"cancel the task called If I Stay", "If I Stay", TaskCancel, "", true},
		{"cancel the task called If I Stay if Mira approves", "If I Stay", TaskCancel, "", false},
		{"cancel task 1 if I stay", "If I Stay", TaskCancel, "", false},
		{"cancel the task called if Mira approves", "If", TaskCancel, "", false},
		{"cancel the task called Don't Ship", "Don't Ship", TaskCancel, "", true},
		{"complete the task called After Lunch", "After Lunch", TaskComplete, "", true},
		{"complete \"After Lunch\"", "After Lunch", TaskComplete, "", true},
		{"rename task 1 to \"Only if Mira approves\"", "Review PR", TaskEdit, "Only if Mira approves", true},
		{"rename task 1 to Do not send the release", "Review PR", TaskEdit, "Do not send the release", true},
	} {
		t.Run(test.input, func(t *testing.T) {
			wire, _ := json.Marshal(Action{Kind: test.kind, Target: "T1", Text: test.text})
			got, err := Decode(string(wire), Request{Input: test.input, Targets: []Target{{Ref: "T1", Title: test.title}}})
			if (err == nil) != test.valid {
				t.Fatalf("title/operation boundary lost: %+v err=%v want valid=%t", got, err, test.valid)
			}
		})
	}
}

func TestModelTaskCreationNeedsCurrentAuthorityBeyondAVerbatimSpan(t *testing.T) {
	for _, test := range []struct{ input, text string }{
		{"Do not create a task to buy milk", "buy milk"},
		{"Don't add a task to buy milk", "buy milk"},
		{"Could you not create a task to buy milk?", "buy milk"},
		{"Mira said: create a task to buy milk", "buy milk"},
		{"\"Create a task to buy milk\"", "buy milk"},
		{"Yesterday I said I need to buy milk", "buy milk"},
		{"If Mira approves, create a task to buy milk", "buy milk"},
		{"Create a task to buy milk if Mira approves", "buy milk"},
		{"Reconcile ingress topology unless Mira objects", "Reconcile ingress topology unless Mira objects"},
		{"I might need to buy milk", "buy milk"},
		{"I should not buy milk", "buy milk"},
		{"Create a task to not send the release", "send the release"},
		{"Save a note: Do not send the release", "Save a note: Do not send the release"},
		{"ask: explain ingress topology", "ask: explain ingress topology"},
		{"421-12", "421-12"},
	} {
		t.Run(test.input, func(t *testing.T) {
			wire, _ := json.Marshal(Action{Kind: Task, Text: test.text})
			if got, err := Decode(string(wire), Request{Input: test.input}); err == nil {
				t.Fatalf("a quoted span alone authorized task creation: %+v", got)
			}
		})
	}
	for _, test := range []struct{ input, text string }{
		{"Reconcile ingress topology", "Reconcile ingress topology"},
		{"I need to reconcile ingress topology", "reconcile ingress topology"},
		{"Create a task to not send the release", "not send the release"},
		{"Create a task called \"Do not send the release\"", "Do not send the release"},
		{"Task: Do not send the release", "Do not send the release"},
		{"Create a task: If Mira approves, send the release", "If Mira approves, send the release"},
		{"Please create a task to review \"If I Stay\"", "review \"If I Stay\""},
		{"Reconcile the \"Do Not Send\" ingress queue", "Reconcile the \"Do Not Send\" ingress queue"},
		{"Add reviewing the Aurora PR to my tasks", "reviewing the Aurora PR"},
	} {
		t.Run(test.input, func(t *testing.T) {
			wire, _ := json.Marshal(Action{Kind: Task, Text: test.text})
			got, err := Decode(string(wire), Request{Input: test.input})
			if err != nil || got.Text != test.text {
				t.Fatalf("real task operation or literal title rejected: %+v err=%v", got, err)
			}
		})
	}
}
