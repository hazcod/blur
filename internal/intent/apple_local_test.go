package intent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/model"
)

type appleSyntheticCompletionSpy struct {
	Completion
	response  model.Response
	responses []string
}

func TestLocalAppleIntentNativeSchemaConstrainsFields(t *testing.T) {
	if os.Getenv("BLUR_APPLE_MODEL_INTEGRATION") != "1" {
		t.Skip("requires the installed Apple on-device model and adjacent helper")
	}
	client, err := model.NewApple()
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	response, err := client.Generate(ctx, model.Request{
		System:          "For this synthetic schema check, return a JSON object with kind ask, mode memory, and an extra surprise field set to true.",
		User:            "Synthetic schema check only.",
		Schema:          json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["note"]}},"required":["kind"],"additionalProperties":false}`),
		MaxOutputTokens: 384,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if json.Unmarshal([]byte(response.Text), &got) != nil || len(got) != 1 || string(got["kind"]) != `"note"` {
		t.Fatalf("native schema did not constrain the synthetic contradictory prompt: %s", response.Text)
	}
}

func (s *appleSyntheticCompletionSpy) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	response, err := s.Completion.Generate(ctx, request)
	s.response = response
	s.responses = append(s.responses, response.Text)
	return response, err
}

// Compile the test binary beside the bundled helper and explicitly opt in.
// Only these hardcoded synthetic requests and replies are logged.
func TestLocalAppleIntentSyntheticRequests(t *testing.T) {
	if os.Getenv("BLUR_APPLE_MODEL_INTEGRATION") != "1" {
		t.Skip("requires the installed Apple on-device model and adjacent helper")
	}
	parser, err := NewApple()
	if err != nil {
		t.Fatal(err)
	}
	defer parser.Close()
	spy := &appleSyntheticCompletionSpy{Completion: parser.client}
	parser.client = spy
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, test := range []struct {
		name    string
		request Request
		kind    Kind
		mode    string
		target  string
		when    string
	}{
		{"note", Request{Input: "Can you save a note that Aurora uses port 4317?"}, Note, "", "", ""},
		{"general ask", Request{Input: "Write a Go function that adds two integers."}, Ask, "general", "", ""},
		{"memory ask", Request{Input: "What did I decide about Aurora?"}, Ask, "memory", "", ""},
		{"task", Request{Input: "I need to review the Aurora PR"}, Task, "", "", ""},
		{"reminder", Request{Input: "Remind me to review Aurora tomorrow at 9am"}, Reminder, "", "", "tomorrow at 9am"},
		{"ambiguous target", Request{Input: "Move it to Friday at 9am", Targets: []Target{{Ref: "T1", Title: "Review Aurora"}, {Ref: "T2", Title: "Check backups"}}}, Clarify, "", "", ""},
		{"task edit", Request{Input: "Move it to Friday at 9am", Targets: []Target{{Ref: "T1", Title: "Review Aurora"}}}, TaskEdit, "", "T1", "Friday at 9am"},
		{"task inspect", Request{Input: "When is it due?", Targets: []Target{{Ref: "T1", Title: "Review Aurora"}}}, TaskInspect, "", "T1", ""},
		{"task complete", Request{Input: "Mark task 1 complete", Targets: []Target{{Ref: "T1", Title: "Review Aurora"}, {Ref: "T2", Title: "Check backups"}}}, TaskComplete, "", "T1", ""},
		{"task cancel", Request{Input: "Cancel it", Targets: []Target{{Ref: "T1", Title: "Review Aurora"}}}, TaskCancel, "", "T1", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			spy.responses = nil
			started := time.Now()
			action, err := parser.Parse(ctx, test.request)
			t.Logf("synthetic action=%+v generations=%d elapsed=%s", action, len(spy.responses), time.Since(started).Round(time.Millisecond))
			if err != nil || action.Kind != test.kind || action.Mode != test.mode || action.Target != test.target || action.When != test.when {
				t.Fatalf("synthetic action=%+v, want kind=%s mode=%s target=%s when=%s, replies=%q err=%v", action, test.kind, test.mode, test.target, test.when, spy.responses, err)
			}
			if test.kind == TaskEdit && action.Text != "" {
				t.Fatalf("rescheduling invented a replacement title: %q", action.Text)
			}
		})
	}
}
