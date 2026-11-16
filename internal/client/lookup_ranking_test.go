package client

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/search"
)

func sameIDs(t *testing.T, got []search.IndexHit, want ...string) {
	t.Helper()
	ids := make([]string, len(got))
	for i, hit := range got {
		ids[i] = hit.SourceID
	}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("order = %v, want %v", ids, want)
	}
}

func TestLookupMajorityTermCount(t *testing.T) {
	for terms, want := range map[int]int{1: 1, 2: 1, 3: 2, 4: 2, 5: 3, 16: 8} {
		if got := lookupMajorityTerms(terms); got != want {
			t.Errorf("lookupMajorityTerms(%d) = %d, want %d", terms, got, want)
		}
	}
}

func TestLookupRankEntityOutranksEverythingElse(t *testing.T) {
	// bit 0 is the entity term; bits 1 and 2 are topic words.
	hits := []search.IndexHit{
		{SourceID: "topic-only-all", MatchedTerms: 0b110},
		{SourceID: "entity-partial", MatchedTerms: 0b011},
	}
	got := rankLookupHits(hits, lookupRankInput{entityMask: 0b001, speech: true, isMessaging: func(id string) bool { return id == "topic-only-all" }})
	sameIDs(t, got, "entity-partial", "topic-only-all")
}

func TestLookupRankSpeechVerbBoostsMessagingApps(t *testing.T) {
	hits := []search.IndexHit{
		{SourceID: "notion", MatchedTerms: 0b111},
		{SourceID: "slack", MatchedTerms: 0b101},
	}
	messaging := func(id string) bool { return id == "slack" }
	sameIDs(t, rankLookupHits(hits, lookupRankInput{speech: true, isMessaging: messaging}), "slack", "notion")
	sameIDs(t, rankLookupHits(hits, lookupRankInput{speech: false, isMessaging: messaging}), "notion", "slack")
}

func TestLookupRankRarityBreaksMatchedCountTies(t *testing.T) {
	hits := []search.IndexHit{
		{SourceID: "common", MatchedTerms: 0b011},
		{SourceID: "rare", MatchedTerms: 0b110},
	}
	sameIDs(t, rankLookupHits(hits, lookupRankInput{rarity: []float64{.1, .2, 3}}), "rare", "common")
}

func TestLookupRankMatchedCountBeforeRarity(t *testing.T) {
	hits := []search.IndexHit{
		{SourceID: "one-rare", MatchedTerms: 0b100},
		{SourceID: "two-common", MatchedTerms: 0b011},
	}
	sameIDs(t, rankLookupHits(hits, lookupRankInput{rarity: []float64{.1, .1, 9}}), "two-common", "one-rare")
}

func TestLookupRankNewestWinsFinalTie(t *testing.T) {
	old, recent := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	hits := []search.IndexHit{
		{SourceID: "a-old", MatchedTerms: 0b11, CapturedAt: old},
		{SourceID: "b-new", MatchedTerms: 0b11, CapturedAt: recent},
	}
	in := lookupRankInput{rarity: []float64{1, 1}}
	sameIDs(t, rankLookupHits(hits, in), "b-new", "a-old")
	// Hits the index could not date are dated on demand.
	undated := []search.IndexHit{{SourceID: "a", MatchedTerms: 0b11}, {SourceID: "b", MatchedTerms: 0b11}}
	in.capturedAt = func(hit search.IndexHit) time.Time {
		if hit.SourceID == "b" {
			return recent
		}
		return old
	}
	sameIDs(t, rankLookupHits(undated, in), "b", "a")
	// Equal times fall back to the source ID so ordering is deterministic.
	hits[1].CapturedAt = old
	sameIDs(t, rankLookupHits(hits, lookupRankInput{rarity: []float64{1, 1}}), "a-old", "b-new")
}

func TestLookupRankCapsThePool(t *testing.T) {
	var hits []search.IndexHit
	for i := 0; i < planLookupPool+25; i++ {
		hits = append(hits, search.IndexHit{SourceID: fmt.Sprintf("s%03d", i), MatchedTerms: 0b11})
	}
	got := rankLookupHits(hits, lookupRankInput{limit: planLookupPool})
	if len(got) != planLookupPool {
		t.Fatalf("pool = %d, want %d", len(got), planLookupPool)
	}
}

func TestLookupEntityFilterIsMandatory(t *testing.T) {
	hits := []search.IndexHit{{SourceID: "with", MatchedTerms: 0b101}, {SourceID: "without", MatchedTerms: 0b110}, {SourceID: "unknown"}}
	sameIDs(t, filterLookupEntity(hits, 0b001), "with")
	if all := filterLookupEntity(hits, 0); len(all) != 3 {
		t.Fatalf("no entity must keep every hit, got %d", len(all))
	}
}

func TestLookupSpeechVerbDetection(t *testing.T) {
	for question, want := range map[string]bool{
		"what did Dima say about the pentest retest?": true,
		"what did Youssef promise?":                   true,
		"what was I told about the invoice":           true,
		"he mentioned the deadline":                   true,
		"where is the API key for venice stored":      false,
		"summarize the orion article":                 false,
	} {
		if got := questionHasSpeechVerb(question); got != want {
			t.Errorf("questionHasSpeechVerb(%q) = %t, want %t", question, got, want)
		}
	}
}

func TestLookupTermRarityFavoursRareTerms(t *testing.T) {
	rare, common := lookupTermRarity(1, 100), lookupTermRarity(90, 100)
	if !(rare > common) || math.Abs(rare-math.Log(101)) > 1e-9 {
		t.Fatalf("rarity rare=%v common=%v", rare, common)
	}
	if got := lookupTermRarity(0, 100); got != 0 {
		t.Fatalf("an absent term carries no rarity, got %v", got)
	}
}
