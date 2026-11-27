package client

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestAskFindsHyphenatedTerminalProductFactWithoutRecallModelRoundTrips(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	text := "Terminal output: threat-bot is running in production. threat-bot version 4.8.2."
	observation, err := capture.SanitizeObservation("com.apple.terminal", "", text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, observation, time.Now()); err != nil {
		t.Fatal(err)
	}
	if more, err := store.IndexPendingExact(ctx, 100); err != nil || more {
		t.Fatalf("index terminal fixture: more=%t err=%v", more, err)
	}
	page, err := store.SearchExact(ctx, search.ExactQuery{Terms: []string{"bot", "production", "running", "threat", "version"}, TopK: 8, MaxRows: search.MaxIndexRows, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != observation.ContentID() {
		t.Fatalf("expected hyphenated exact index hit before Ask: page=%+v err=%v", page, err)
	}
	model := &singleSearchModel{answer: assistant.ModelAnswer{Text: "Threat Bot is running version 4.8.2 in production.", SourceIDs: []string{observation.ContentID()}}}
	c := NewClient(store)
	c.localAI = &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}, askModel: model, service: notes.NewService(store, nil, nil)}
	t.Cleanup(c.Close)
	service, err := reminders.NewService(store, &clientNoticePort{}, reminders.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.reminders = service
	t.Cleanup(service.Close)
	if _, err := c.CreateTask(ctx, "Review unrelated build pipeline", nil); err != nil {
		t.Fatal(err)
	}

	result, err := c.AskMemory(ctx, "What version of threat bot is production running?")
	if err != nil || result.Answer.InsufficientEvidence || len(result.Sources) != 1 || result.Sources[0].SourceID != observation.ContentID() || !strings.Contains(result.Answer.Text, "4.8.2") {
		t.Fatalf("Ask failed to answer from the matching terminal text: result=%+v err=%v", result, err)
	}
	if model.calls != 0 || model.answerCalls != 1 {
		t.Fatalf("specific exact technical recall should use one answer generation, not model-driven recall probes: recall_calls=%d answer_calls=%d", model.calls, model.answerCalls)
	}
}
