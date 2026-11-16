package intent

import (
	"encoding/json"
	"testing"
)

func TestCompactRelativeSchedulesKeepAuthorityAndVerbatimSpans(t *testing.T) {
	for _, input := range []string{
		"in 5m i need to pickup Jane", "I need to pickup Jane in 5 mins", "in 5min: pickup Jane",
		"in 2hrs: pick up Jane", "I need to pick up Jane in 1d", "in 30secs I should pick up Jane",
	} {
		action, ok := PersonalReminder(input)
		if !ok || action.Kind != Reminder || action.Text == "" || action.When == "" {
			t.Errorf("compact schedule wasn't locally understood: %q => %+v", input, action)
			continue
		}
		wire, _ := json.Marshal(action)
		if _, err := Decode(string(wire), Request{Input: input}); err != nil {
			t.Errorf("compact schedule failed its final authority check: %q => %v", input, err)
		}
	}
	for _, input := range []string{
		"in 5m I should not pickup Jane", "in 5m what should I do", "in 5m: Jane arrives",
		"Jane said in 5m pickup Jane", "If it stops raining, I need to pickup Jane in 5m", "\"in 5m: pickup Jane\"",
	} {
		if action, ok := PersonalReminder(input); ok {
			t.Errorf("compact units bypassed authority: %q => %+v", input, action)
		}
	}
}

func TestASecondSchedulingSpanCannotBeSilentlyDropped(t *testing.T) {
	for _, test := range []struct{ input, text, when string }{
		{"in 5m I need to pickup Jane tomorrow", "pickup Jane tomorrow", "in 5m"},
		{"I need to pickup Jane in 5m at 9am", "pickup Jane in 5m", "at 9am"},
		{"tomorrow I need to pickup Jane in 5m", "pickup Jane in 5m", "tomorrow"},
		{"I need to pickup Jane tomorrow at 9am in 5m", "pickup Jane tomorrow at 9am", "in 5m"},
	} {
		if action, ok := PersonalReminder(test.input); ok {
			t.Errorf("conflicting schedule became task title data: %q => %+v", test.input, action)
		}
		wire, _ := json.Marshal(Action{Kind: Reminder, Text: test.text, When: test.when})
		if _, err := Decode(string(wire), Request{Input: test.input}); err == nil {
			t.Errorf("model dropped a conflicting schedule: %q", test.input)
		}
	}
}

func TestCurrentDaypartsRecognizeOnlyCurrentActionAuthority(t *testing.T) {
	for _, test := range []struct{ input, text, when string }{
		{"this morning I need to pickup Jane", "pickup Jane", "this morning"},
		{"This afternoon I need to review the Aurora PR", "review the Aurora PR", "This afternoon"},
		{"this evening: get eggs", "get eggs", "this evening"},
		{"I need to get eggs this morning", "get eggs", "this morning"},
		{"morning: get eggs", "get eggs", "morning"},
		{"this morning at 1:30pm I need to pickup Jane", "pickup Jane", "this morning at 1:30pm"},
	} {
		action, ok := PersonalReminder(test.input)
		if !ok || action.Kind != Reminder || action.Text != test.text || action.When != test.when {
			t.Errorf("daypart action was not recognized: %q => %+v", test.input, action)
		}
		wire, _ := json.Marshal(Action{Kind: Reminder, Text: test.text, When: test.when})
		if _, err := Decode(string(wire), Request{Input: test.input}); err != nil {
			t.Errorf("grounded daypart action failed validation: %q => %v", test.input, err)
		}
	}
	for _, input := range []string{"this morning I bought eggs", "this afternoon: Jane arrived", "this evening was busy", "This morning what should I do?", "this afternoon I should not buy eggs", "Mira said: this evening get eggs", "\"this morning I need to get eggs\""} {
		if action, ok := PersonalReminder(input); ok {
			t.Errorf("daypart observation/quote/question became an action: %q => %+v", input, action)
		}
	}
}
