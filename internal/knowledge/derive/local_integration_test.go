package derive

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/model"
)

func TestLocalOllamaSourceLinkedFactExtraction(t *testing.T) {
	if os.Getenv("BLUR_LOCAL_MODEL_INTEGRATION") != "1" {
		t.Skip("requires a real local answer model")
	}
	name := os.Getenv("BLUR_LOCAL_TEST_MODEL")
	if name == "" {
		name = "qwen3:14b"
	}
	g, err := NewOllamaGenerator("http://127.0.0.1:11434", name)
	if err != nil {
		t.Fatal(err)
	}
	defer g.CloseIdleConnections()
	input := []Evidence{{SourceID: strings.Repeat("a", 64), Text: "Project Atlas is owned by Mina. Its deployment window is Tuesday."}}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	proposals, err := g.Generate(ctx, input)
	if err != nil || len(proposals) == 0 || ValidateSuggestions(input, proposals) != nil {
		t.Fatalf("real local fact extraction: %+v err=%v", proposals, err)
	}
}

func TestLocalOllamaMultipleFactsWithRealisticSourceID(t *testing.T) {
	if os.Getenv("BLUR_LOCAL_MODEL_INTEGRATION") != "1" {
		t.Skip("requires a real local answer model")
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
	text := "Project Atlas is owned by Mina. Atlas uses Rust for its desktop app."
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	input := []Evidence{{SourceID: id, Text: text}}
	encoded, err := encodeInput(input)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	response, err := client.Generate(ctx, model.Request{System: systemPrompt, User: string(encoded), Format: "json", MaxOutputTokens: MaxOutputTokens})
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := decodeLinkedResponse(response.Text, input)
	if err != nil || ValidateSuggestions(input, proposals) != nil {
		t.Fatalf("synthetic model response tokens=%d response=%s err=%v", response.OutputTokens, response.Text, err)
	}
}
