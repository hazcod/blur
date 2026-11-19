package client

import (
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/search"
)

func TestLookupRankClosenessBreaksRarityTiesBeforeCaptureTime(t *testing.T) {
	now := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	hits := []search.IndexHit{
		{SourceID: "newer-but-scattered", MatchedTerms: 0b11, CapturedAt: now},
		{SourceID: "older-but-tight", MatchedTerms: 0b11, CapturedAt: now.Add(-24 * time.Hour)},
		{SourceID: "unknown-closeness", MatchedTerms: 0b11, CapturedAt: now.Add(time.Hour)},
	}
	closeness := map[string]int{"newer-but-scattered": 400, "older-but-tight": 30}
	in := lookupRankInput{closeness: func(hit search.IndexHit) int { return closeness[hit.SourceID] }}
	sameIDs(t, rankLookupHits(hits, in), "older-but-tight", "newer-but-scattered", "unknown-closeness")
	// Without the hook the order is unchanged: capture time decides.
	sameIDs(t, rankLookupHits(hits, lookupRankInput{}), "unknown-closeness", "newer-but-scattered", "older-but-tight")
	// Matched-term count and rarity still outrank closeness.
	hits = []search.IndexHit{{SourceID: "tight-partial", MatchedTerms: 0b01}, {SourceID: "loose-full", MatchedTerms: 0b11}}
	closeness = map[string]int{"tight-partial": 5, "loose-full": 500}
	sameIDs(t, rankLookupHits(hits, in), "loose-full", "tight-partial")
}

func TestTermClosenessIsTheTightestWindowCoveringEveryPresentTerm(t *testing.T) {
	text := "dima " + strings.Repeat("filler ", 30) + "dima and the retest, again filler"
	if got := termCloseness(text, []string{"dima", "retest"}); got != len("dima and the retest") {
		t.Fatalf("closeness = %d, want %d", got, len("dima and the retest"))
	}
	if got := termCloseness("only dima here", []string{"dima", "retest"}); got != len("dima") {
		t.Fatalf("closeness with one present term = %d", got)
	}
	if got := termCloseness("nothing relevant", []string{"dima"}); got != 0 {
		t.Fatalf("closeness without a match = %d, want 0 (unknown)", got)
	}
}
