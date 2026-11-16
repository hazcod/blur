package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// Synthetic content, temporary encrypted data/settings and a verified local
// model only. This never opens the user's memory or changes app preferences.
func TestLocalAskFindsFactAtEndOfLargePage(t *testing.T) {
	if os.Getenv("BLUR_LOCAL_MODEL_INTEGRATION") != "1" {
		t.Skip("requires a local answer model")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	directory := t.TempDir()
	store, err := durable.OpenDurable(filepath.Join(directory, "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	noteService := notes.NewService(store, nil, nil)
	controller := newLocalAIController(filepath.Join(directory, "local-ai.json"), noteService)
	defer controller.Close()
	c := NewClient(store).WithNotes(noteService).WithLocalAI(controller)
	defer c.Close()
	name := os.Getenv("BLUR_LOCAL_TEST_MODEL")
	if name == "" {
		name = "qwen3:14b"
	}
	if err := c.ConfigureLocalAI(ctx, LocalAISettings{Enabled: true, Provider: ProviderOllama, Endpoint: "http://127.0.0.1:11434", Model: name}); err != nil {
		t.Fatal(err)
	}
	prefix := exactLargeFixture(t, 20000).Text()
	text := prefix + "\nThe database release gate is a successful restore rehearsal on the staging cluster. The owner of this release gate is Mira."
	o, err := capture.SanitizeObservation("browser.app", "docs.example", text, capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservation(ctx, o); err != nil {
		t.Fatal(err)
	}
	if more, err := store.IndexPendingExact(ctx, 400); err != nil || more {
		t.Fatal(more, err)
	}
	result, err := c.AskMemory(ctx, "What did I read about the database release gate?")
	if err != nil || result.Answer.InsufficientEvidence || result.IndexingPending || len(result.Sources) != 1 || result.Sources[0].SourceID != o.ContentID() || result.Sources[0].MatchStart <= 0 || !strings.Contains(strings.ToLower(result.Answer.Text), "restore") || result.Truncated == 0 {
		t.Fatalf("large-page tail fact was not retrieved and cited: %+v %v", result, err)
	}
	if len(result.Sources[0].Text) != len(o.Text()) {
		t.Fatal("source inspection lost original content")
	}
}
