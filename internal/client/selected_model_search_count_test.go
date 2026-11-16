package client

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/assistant"
)

type structuredRecallSpy struct {
	taskListModelSpy
	structured int
	schema     json.RawMessage
	err        error
}

func (m *structuredRecallSpy) GenerateGeneralJSONPrompt(_ context.Context, _, _ string, schema json.RawMessage, _ int) (string, error) {
	m.structured++
	m.schema = append(json.RawMessage(nil), schema...)
	return `{"matches":[]}`, m.err
}

func TestStructuredRecallAvoidsRedundantPlainTokenizerAndRetainsSchemaBudget(t *testing.T) {
	schema := json.RawMessage(recallQueriesSchema)
	for _, providerErr := range []error{nil, assistant.ErrTokenBudget} {
		m := &structuredRecallSpy{err: providerErr}
		_, err := recallGenerate(context.Background(), m, m, "bounded system", map[string]string{"question": "synthetic memory"}, schema)
		if !errors.Is(err, providerErr) || m.calls.Load() != 0 || m.structured != 1 || string(m.schema) != string(schema) {
			t.Fatalf("structured recall repeated counting, lost schema, or hid exact budget rejection: plain=%d structured=%d err=%v", m.calls.Load(), m.structured, err)
		}
	}
	for _, oversized := range []bool{false, true} {
		m := &structuredRecallSpy{}
		ctx, cancel := context.WithCancel(context.Background())
		payload := "synthetic"
		if oversized {
			payload = strings.Repeat("x", recallPromptBytes)
		} else {
			cancel()
		}
		_, err := recallGenerate(ctx, m, m, "system", payload, schema)
		cancel()
		if err == nil || m.calls.Load() != 0 || m.structured != 0 {
			t.Fatal("canceled/oversized recall reached selected provider", err)
		}
	}
}
