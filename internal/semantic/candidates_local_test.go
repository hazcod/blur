package semantic_test

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/semantic"
)

// This exercises an already installed, verified local model with synthetic
// memory only. It never pulls a model, reads the user's database, or changes
// provider settings. The fixture deliberately includes competing nearby ideas.
func TestLocalOllamaSavedTasksAndNotesRecallSynonymsAndPhrases(t *testing.T) {
	if os.Getenv("BLUR_EMBEDDING_INTEGRATION") != "1" {
		t.Skip("set BLUR_EMBEDDING_INTEGRATION=1 to verify the installed local embedding model")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	embedder, err := semantic.NewOllamaEmbedder("http://127.0.0.1:11434", semantic.DefaultEmbeddingModel, semantic.DefaultDimensions)
	if err != nil {
		t.Fatal(err)
	}
	store := serviceStore(t)
	s, err := semantic.NewService(store, store, embedder, semantic.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	identity, err := embedder.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("installed model=%s digest=%s dimensions=%d format=%s", identity.Name, identity.Digest, identity.Dimensions, identity.Format)
	texts := []string{
		"Drycleanres on monday",
		"Dentist appointment on Monday",
		"Buy milk at the supermarket",
		"Finish the quarterly financial report",
		"Pick up a package at the post office",
		"Clean the kitchen on Monday",
		"Book flights to London",
		"Wash the car this weekend",
		"Call the bank about mortgage rates",
		"Review a pull request",
		"Renew my gym membership",
		"Send the contract to the lawyer",
		"Laundry detergent shopping",
		"Take the dog to the veterinarian",
		"Check the weather for Monday",
		"Buy new clothes online",
	}
	tests := []struct {
		query         string
		want, maxRank int
	}{
		{"drycleaning", 0, 1},
		{"dry cleaners", 0, 1},
		{"pick up my clothes", 0, 3},
		{"collect my laundry", 0, 3},
		{"cleaning my suit", 0, 2},
		{"fix a tooth", 1, 2},
		{"grocery shopping", 2, 2},
		{"collect parcel", 4, 1},
		{"exercise subscription", 10, 1},
		{"home loan interest", 8, 1},
	}
	noteService := notes.NewService(store, nil, nil)
	noteIDs := make([]string, len(texts))
	for i, text := range texts {
		result, err := noteService.Add(ctx, text)
		if err != nil {
			t.Fatal(err)
		}
		noteIDs[i] = result.SourceID
	}
	if err := s.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			scores, err := s.RankCandidates(ctx, test.query, texts)
			if err != nil || len(scores) != len(texts) {
				t.Fatalf("task scores=%v err=%v", scores, err)
			}
			order := make([]int, len(texts))
			for i := range order {
				order[i] = i
			}
			sort.SliceStable(order, func(i, j int) bool { return scores[order[i]] > scores[order[j]] })
			rank := 0
			for i, candidate := range order {
				if candidate == test.want {
					rank = i + 1
				}
				t.Logf("task rank=%02d cosine=%.4f text=%q", i+1, scores[candidate], texts[candidate])
			}
			if rank < 1 || rank > test.maxRank {
				t.Errorf("task relevance: wanted %q in top %d, rank=%d", texts[test.want], test.maxRank, rank)
			}
			hits, err := s.Search(ctx, test.query, semantic.Query{Limit: len(texts)})
			if err != nil {
				t.Fatal(err)
			}
			noteRank := 0
			for i, hit := range hits {
				if hit.SourceID == noteIDs[test.want] {
					noteRank = i + 1
				}
			}
			if noteRank < 1 || noteRank > test.maxRank {
				t.Errorf("persisted note relevance: wanted %q in top %d, rank=%d", texts[test.want], test.maxRank, noteRank)
			}
			if strings.Contains(test.query, "clothes") {
				t.Log("The pickup phrase is ambiguous: buying clothes and collecting a package are valid competing concepts; this fixture promises top-three recall, not rank one.")
			}
		})
	}
}
