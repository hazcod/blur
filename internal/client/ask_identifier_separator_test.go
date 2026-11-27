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
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestTechnicalSubjectSupportIgnoresSeparatorSpelling(t *testing.T) {
	for _, text := range []string{"threat-bot running", "Threat Bot running", "THREAT_BOT", "threat\u2011bot", "ThreatBot running"} {
		if !technicalSubjectSupport([]string{"threat_bot"}, text) {
			t.Fatal("separator spelling rejected", text)
		}
	}
	for _, text := range []string{"threatbots", "threat–bot", "threat_ bot", "threat-bot-v2", "threat_bot.py", "threat.bot", "threat\nbot"} {
		if technicalSubjectSupport([]string{"threat_bot"}, text) {
			t.Fatal("different identifier accepted", text)
		}
	}
}

func TestTechnicalSubjectSpanMapsOriginalBytesAcrossSeparatorsAndAccents(t *testing.T) {
	text := "É café-bot tourne"
	start, end, found := technicalSubjectSpan("cafe_bot", text)
	if !found || text[start:end] != "café-bot" {
		t.Fatalf("span %d..%d found=%t", start, end, found)
	}
}

func TestAskFindsUnderscoreQuestionForAnyStoredSeparatorSpelling(t *testing.T) {
	question := "What version threat_bot production running?"
	for _, spelling := range []string{"threat-bot", "Threat Bot", "threat bot", "threat_bot", "ThreatBot", "threatbot"} {
		t.Run(spelling, func(t *testing.T) {
			ctx := context.Background()
			store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			target, err := capture.SanitizeObservation("com.apple.terminal", "", spelling+" running in production. "+spelling+" version 4.8.2.", 4096)
			if err != nil {
				t.Fatal(err)
			}
			lookalike, err := capture.SanitizeObservation("com.apple.terminal", "", "threat-bot-v2 running in production. threat-bot-v2 version 9.9.9.", 4096)
			if err != nil {
				t.Fatal(err)
			}
			for _, observation := range []capture.SanitizedObservation{target, lookalike} {
				if err := store.PutObservationAt(ctx, observation, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			if more, err := store.IndexPendingExact(ctx, 100); err != nil || more {
				t.Fatalf("index fixture: more=%t err=%v", more, err)
			}
			model := &singleSearchModel{answer: assistant.ModelAnswer{Text: spelling + " is running version 4.8.2.", SourceIDs: []string{target.ContentID()}}}
			c := NewClient(store)
			c.localAI = &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}, askModel: model, service: notes.NewService(store, nil, nil)}
			t.Cleanup(c.Close)

			result, err := c.AskMemory(ctx, question)
			if err != nil || result.Answer.InsufficientEvidence || len(result.Sources) != 1 || result.Sources[0].SourceID != target.ContentID() || !strings.Contains(result.Answer.Text, "4.8.2") {
				t.Fatalf("underscore question abstained for stored spelling: err=%v", err)
			}
		})
	}
}
