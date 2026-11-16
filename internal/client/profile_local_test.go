package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/profile"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// Synthetic authored records only; this never opens the user's store or
// changes their model settings. It exercises the normal encrypted-client Ask.
func TestLocalExplicitFactsAndScopedWritingRules(t *testing.T) {
	if os.Getenv("BLUR_LOCAL_MODEL_INTEGRATION") != "1" {
		t.Skip("requires a local answer model")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	store, err := durable.OpenDurable(filepath.Join(dir, "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	noteService := notes.NewService(store, nil, nil)
	controller := newLocalAIController(filepath.Join(dir, "local-ai.json"), noteService)
	c := NewClient(store).WithNotes(noteService).WithLocalAI(controller)
	defer controller.Close()
	defer c.Close()
	name := os.Getenv("BLUR_LOCAL_TEST_MODEL")
	if name == "" {
		name = "qwen3:14b"
	}
	if err := c.ConfigureLocalAI(ctx, LocalAISettings{Enabled: true, Provider: ProviderOllama, Endpoint: "http://127.0.0.1:11434", Model: name}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateProfile(ctx, profile.Fact, "My preferred language for reports is Dutch.", profile.Scope{}); err != nil {
		t.Fatal(err)
	}
	rule, err := c.CreateProfile(ctx, profile.WritingRule, "Write the answer in Dutch.", profile.Scope{Project: "Atlas", Audience: "engineering team"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.AskMemory(ctx, "Which language should I use for reports?")
	if err != nil || result.Answer.InsufficientEvidence || len(result.Sources) != 1 || result.Sources[0].Origin != assistant.OriginUserFact || !strings.Contains(strings.ToLower(result.Answer.Text), "dutch") {
		t.Fatalf("explicit fact was not retrieved/cited: %+v %v", result, err)
	}
	result, err = c.AskMemory(ctx, "Explain a hash map for the engineering team on Atlas in three short numbered points.")
	if err != nil || !result.General || result.AppliedRules != 1 || len(result.Sources) != 0 || (!strings.Contains(strings.ToLower(result.Answer.Text), "een ") && !strings.Contains(strings.ToLower(result.Answer.Text), "gegevens")) {
		t.Fatalf("scoped Dutch style did not apply: %+v %v", result, err)
	}
	changed := rule.Record
	changed.Enabled = false
	if _, err := c.UpdateProfile(ctx, changed); err != nil {
		t.Fatal(err)
	}
	result, err = c.AskMemory(ctx, "Explain a hash map for the engineering team on Atlas in one short sentence.")
	if err != nil || !result.General || result.AppliedRules != 0 {
		t.Fatalf("paused rule remained active: %+v %v", result, err)
	}
	result, err = c.AskMemory(ctx, "Explain database isolation in six numbered paragraphs, with at least two short sentences per paragraph. Number them 1. through 6.")
	if err != nil || !result.General || len(result.Answer.Text) < 600 || !strings.Contains(result.Answer.Text, "6.") {
		t.Fatalf("detailed general request was cut short: %+v %v", result, err)
	}
}
