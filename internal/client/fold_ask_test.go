package client

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func foldAskStore(t *testing.T) *durable.Store {
	t.Helper()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestAccentedNameIdentityQuestionAnswersExtractivelyFromCapture(t *testing.T) {
	ctx := context.Background()
	store := foldAskStore(t)
	text := "Notes from José about the résumé: he joins the platform team next month."
	observation, err := capture.SanitizeObservation("com.apple.Notes", "", text, capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.UTC)
	if err := store.PutObservationAt(ctx, observation, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	scenarioIndex(t, store)
	c := NewClient(store)
	c.askNow = func() time.Time { return now }
	defer c.Close()
	for _, question := range []string{"What or who is José?", "what or who is Jose?", "What do you know about José?"} {
		t.Run(question, func(t *testing.T) {
			result, err := c.AskMemory(ctx, question)

			if err != nil || !result.Direct || result.General || len(result.Sources) != 1 || result.Sources[0].SourceID != observation.ContentID() || !strings.HasPrefix(result.Answer.Text, "Your saved source says: ") || !strings.Contains(result.Answer.Text, "José") {
				t.Fatal("accented name lost its extractive answer", result, err)
			}
		})
	}
}
