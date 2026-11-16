package intent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/model"
)

func TestLocalIntentSchemaForTechnicalRequest(t *testing.T) {
	if os.Getenv("BLUR_LOCAL_MODEL_INTEGRATION") != "1" {
		t.Skip("requires a local answer model")
	}
	name := os.Getenv("BLUR_LOCAL_TEST_MODEL")
	if name == "" {
		name = "qwen3:14b"
	}
	client, err := model.NewOllama("http://127.0.0.1:11434", name)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, request := range []Request{{Input: "Write a Go function that reads a file with context cancellation."}, {Input: "I need to review the PR"}} {
		data, _ := json.Marshal(request)
		response, err := client.Generate(ctx, model.Request{System: parserSystem, User: string(data), Schema: actionSchema(request), MaxOutputTokens: 384})
		if err != nil {
			t.Fatal(err)
		}
		action, err := Decode(response.Text, request)
		if err != nil {
			t.Fatalf("synthetic intent response tokens=%d content=%s action=%+v error=%v", response.OutputTokens, response.Text, action, err)
		}
	}
}
