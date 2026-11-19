package client

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// A common term held by far more sources than the row budget allows cannot have
// its majority-term top results proven. That widening must degrade to the
// all-terms hits plus a coverage gap, never fail the question.
func TestLookupMajorityPassOverTheRowBudgetKeepsAllTermsHitsAndReportsLimitedCoverage(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	put := func(text string) string {
		observation, err := capture.SanitizeObservation("com.example.Editor", "docs.example", text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservation(ctx, observation); err != nil {
			t.Fatal(err)
		}
		return observation.ContentID()
	}
	// Both terms are common. Every source that has one has the other, so the
	// partial-only pass matches nothing, can never fill its top-K and must scan
	// until the row budget stops it.
	put("zebra quokka planning notes")
	for i := 0; i < 700; i++ {
		put(fmt.Sprintf("zebra quokka capture number %d", i))
	}

	// The control: the widened query really does exhaust the row budget here.
	_, err = store.SearchExact(ctx, search.ExactQuery{Terms: []string{"quokka", "zebra"}, MinTermMatches: 1, PartialOnly: true, TopK: planLookupPool, MaxRows: min(search.MaxIndexRows, planLookupPool*16), MaxBytes: search.MaxIndexBytes})
	if !errors.Is(err, search.ErrIndexBudget) {
		t.Fatalf("fixture must exhaust the row budget on the partial pass, got %v", err)
	}

	c := NewClient(store)
	defer c.Close()
	plan := assistant.Plan{Kind: assistant.KindLookup, Terms: []string{"quokka", "zebra"}, Question: "what are the zebra quokka notes"}
	page, err := c.planSearch(ctx, plan, false, nil)
	if err != nil {
		t.Fatalf("a budget-limited widening must not fail the question: %v", err)
	}
	if !page.CoverageLimited || !page.HasMore {
		t.Fatalf("the skipped widening must be reported: coverageLimited=%t hasMore=%t", page.CoverageLimited, page.HasMore)
	}
	if len(page.Hits) == 0 {
		t.Fatal("the all-terms hits must survive the skipped widening")
	}
	for _, hit := range page.Hits {
		if hit.MatchedTerms != 0b11 {
			t.Fatalf("only all-terms hits remain once the widening is skipped: %+v", hit)
		}
	}
	retrieval, err := c.RetrievePlan(ctx, plan)
	if err != nil || len(retrieval.Evidence) == 0 || retrieval.Diagnosis.SemanticIssue != "limited" {
		t.Fatalf("retrieval err=%v evidence=%d semantic=%q, want evidence with a limited-coverage diagnosis", err, len(retrieval.Evidence), retrieval.Diagnosis.SemanticIssue)
	}
}
