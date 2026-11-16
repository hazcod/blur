package intent

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/model"
)

type naturalIntentResponses struct {
	Completion
	texts []string
}

func (s *naturalIntentResponses) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	response, err := s.Completion.Generate(ctx, request)
	s.texts = append(s.texts, response.Text)
	return response, err
}

// Opt-in synthetic language checks exercise a real selected local provider.
// They never receive memory, credentials, captured text or real task targets.
func TestLocalNaturalIntentContrasts(t *testing.T) {
	if os.Getenv("BLUR_LOCAL_MODEL_INTEGRATION") != "1" {
		t.Skip("requires an installed local answer model")
	}
	name := os.Getenv("BLUR_LOCAL_TEST_MODEL")
	if name == "" {
		name = "qwen2.5:14b"
	}
	parser, err := NewOllama("http://127.0.0.1:11434", name)
	if err != nil {
		t.Fatal(err)
	}
	defer parser.Close()
	responses := &naturalIntentResponses{Completion: parser.client}
	parser.client = responses
	for _, test := range []struct {
		input string
		kind  Kind
	}{
		{"Monday: get eggs", Reminder},
		{"reconcile ingress topology next Monday", Reminder},
		{"Tomorrow: spline-fit the pressure data", Reminder},
		{"triage latency regressions on Friday", Reminder},
		{"ask Mira who owns the rollback on Monday", Reminder},
		{"I need to compare the two crash reports on Monday", Reminder},
		{"Mira owns the rollback on Monday", Note},
		{"Monday: Noor prefers tea without sugar", Note},
		{"I bought eggs on Monday", Note},
		{"I need to review the Aurora PR", Task},
		{"Can you jot this down: the Aurora owner is Mira?", Note},
		{"What is a rollback owner?", Ask},
	} {
		t.Run(test.input, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			started := time.Now()
			responses.texts = nil
			action, err := parser.Parse(ctx, Request{Input: test.input})
			t.Logf("synthetic action=%+v replies=%q elapsed=%s", action, responses.texts, time.Since(started))
			if err != nil || action.Kind != test.kind {
				t.Fatalf("natural intent=%+v want=%s err=%v", action, test.kind, err)
			}
		})
	}
}
