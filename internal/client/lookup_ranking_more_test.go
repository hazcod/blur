package client

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/search"
)

func TestLookupRankDemotionHappensBeforeTheCut(t *testing.T) {
	// Nine sign-in pages outrank four substantive captures. After a cut to 8 the
	// substance would be gone; demoting first keeps all four.
	var hits []search.IndexHit
	for i := 0; i < 9; i++ {
		hits = append(hits, search.IndexHit{SourceID: fmt.Sprintf("login%d", i), MatchedTerms: 0b11})
	}
	for i := 0; i < 4; i++ {
		hits = append(hits, search.IndexHit{SourceID: fmt.Sprintf("work%d", i), MatchedTerms: 0b01})
	}
	isLogin := func(hit search.IndexHit) bool { return strings.HasPrefix(hit.SourceID, "login") }
	got := rankLookupHits(hits, lookupRankInput{demote: isLogin, limit: 8})
	if len(got) != 4 {
		t.Fatalf("got %d hits, want the 4 substantive captures: %v", len(got), got)
	}
	// Without a demotion hook the cut keeps only the sign-in pages.
	if cut := rankLookupHits(hits, lookupRankInput{limit: 8}); !strings.HasPrefix(cut[0].SourceID, "login") || len(cut) != 8 {
		t.Fatal("control: the undemoted cut should be filled by sign-in pages")
	}
	// Nothing substantive: keep the sign-in pages.
	if got := rankLookupHits(hits[:9], lookupRankInput{demote: isLogin, limit: 8}); len(got) != 8 {
		t.Fatalf("a pool of only sign-in pages must survive demotion, got %d", len(got))
	}
}

func TestIsConversationalSourceTreatsOnlySpokenTranscriptsAsSpeech(t *testing.T) {
	cases := []struct {
		app, domain string
		spoken      bool
		want        bool
	}{
		{"com.tinyspeck.slackmacgap", "app.slack.com", false, true},
		{"com.apple.mail", "", false, true},
		{meetingsAppID, "abc.meeting.local", true, true},
		{meetingsAppID, "abc.meeting.local", false, false}, // the user's own meeting note
		{"com.notion.id", "notion.so", false, false},
	}
	for _, tc := range cases {
		if got := isConversationalSource(tc.app, tc.domain, tc.spoken); got != tc.want {
			t.Errorf("isConversationalSource(%q,%q,%t)=%t want %t", tc.app, tc.domain, tc.spoken, got, tc.want)
		}
	}
}

func TestLookupRankTranscriptAndMessagingTieBreakDeterministically(t *testing.T) {
	older, newer := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC), time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	conversational := func(string) bool { return true }
	build := func() []search.IndexHit {
		return []search.IndexHit{
			{SourceID: "voice", MatchedTerms: 0b10, CapturedAt: newer},
			{SourceID: "slack", MatchedTerms: 0b10, CapturedAt: older},
		}
	}
	in := lookupRankInput{speech: true, isMessaging: conversational, rarity: []float64{1, 1}}
	a := rankLookupHits(build(), in)
	reversed := build()
	reversed[0], reversed[1] = reversed[1], reversed[0]
	b := rankLookupHits(reversed, in)
	sameIDs(t, a, "voice", "slack")
	sameIDs(t, b, "voice", "slack")
	// A messaging capture beats a user's own meeting note that matches equally.
	notes := func(id string) bool { return id == "slack" }
	sameIDs(t, rankLookupHits(build(), lookupRankInput{speech: true, isMessaging: notes}), "slack", "voice")
}

type failingProbeData struct{ *testData }

func (d failingProbeData) SearchExact(ctx context.Context, q search.ExactQuery) (search.ExactPage, error) {
	if len(q.Terms) == 1 {
		return search.ExactPage{}, search.ErrIndexBudget
	}
	return d.testData.SearchExact(ctx, q)
}

func TestTermDocumentFrequencyFallsBackToCommonWhenTheProbeIsBudgetLimited(t *testing.T) {
	c := NewClient(failingProbeData{&testData{}})
	df, err := c.termDocumentFrequency(context.Background(), "alpha")
	if err != nil || df != search.MaxIndexRows {
		t.Fatalf("df=%d err=%v, want the common-term fallback %d without failing", df, err, search.MaxIndexRows)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.termDocumentFrequency(ctx, "alpha"); err == nil {
		t.Fatal("a canceled context must still surface")
	}
}
