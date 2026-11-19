package client

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// libraryNameFixture stores one inferred Library entity per name. The durable
// name index is keyed by knowledge.NormalizeExact (lowercase only) and is never
// migrated, so lookups must offer the stored spelling at the boundary.
func libraryNameFixture(t *testing.T, names ...string) *Client {
	t.Helper()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c := NewClient(s)
	t.Cleanup(c.Close)
	for _, name := range names {
		text := name + " owns the vendor contract renewal."
		source, err := capture.SanitizeObservation("editor.app", "work.example", text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(context.Background(), source, time.Now()); err != nil {
			t.Fatal(err)
		}
		chunk := enrich.Chunk{SourceID: source.ContentID(), ModelKey: enrich.ModelKey("fixture"), End: len(text), Next: len(text), Complete: true,
			Proposals: []derive.Proposal{{Kind: knowledge.Person, Name: name, Fact: text, SourceID: source.ContentID()}}}
		if err := s.ApplyEnrichment(context.Background(), chunk); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func libraryFacts(t *testing.T, c *Client, question string) []string {
	t.Helper()
	plan, err := assistant.PlanQuestion(question, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.RetrievePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	var facts []string
	for _, item := range retrieval.Evidence {
		if item.Origin == assistant.OriginInferred {
			facts = append(facts, item.Text)
		}
	}
	return facts
}

func hasFact(facts []string, want string) bool {
	return slices.ContainsFunc(facts, func(fact string) bool { return strings.Contains(fact, want) })
}

func TestTopicQuestionFindsLibraryEntityStoredWithAccents(t *testing.T) {
	c := libraryNameFixture(t, "José Müller")
	for _, question := range []string{"What did we decide about José Müller?", "what did we decide about JOSÉ MÜLLER"} {
		if facts := libraryFacts(t, c, question); !hasFact(facts, "José Müller owns") {
			t.Fatalf("%q lost the accented Library entity: %v", question, facts)
		}
	}
}

func TestTopicQuestionFindsAsciiLibraryEntityFromAccentedQuestion(t *testing.T) {
	c := libraryNameFixture(t, "Jose Muller")
	for _, question := range []string{"What did we decide about José Müller?", "What did we decide about Jose Muller?"} {
		if facts := libraryFacts(t, c, question); !hasFact(facts, "Jose Muller owns") {
			t.Fatalf("%q lost the plain-spelling Library entity: %v", question, facts)
		}
	}
}

func TestEntityQuestionFindsAsciiLibraryEntityFromAccentedName(t *testing.T) {
	c := libraryNameFixture(t, "Jose")
	plan, err := assistant.PlanQuestion("What do you know about José?", time.Now(), nil)
	if err != nil || plan.Entity == "" {
		t.Fatalf("plan: %+v err=%v", plan, err)
	}
	evidence, err := c.inferredEntityEvidence(context.Background(), plan)
	if err != nil || !hasFact(func() []string {
		var texts []string
		for _, item := range evidence {
			texts = append(texts, item.Text)
		}
		return texts
	}(), "Jose owns") {
		t.Fatalf("entity lookup missed the plain-spelling Library entity: %+v err=%v", evidence, err)
	}
}

func TestTopicKnowledgeQueriesOfferFoldedAndOriginalSpellings(t *testing.T) {
	queries := topicKnowledgeQueries([]string{"jose", "muller"}, "what did we decide about José Müller?")
	for _, want := range []string{"jose muller", "josé müller", "jose", "josé", "muller", "müller"} {
		if !slices.Contains(queries, want) {
			t.Fatalf("missing %q in %q", want, queries)
		}
	}
	seen := map[string]bool{}
	for _, query := range queries {
		if seen[query] {
			t.Fatalf("duplicate %q in %q", query, queries)
		}
		seen[query] = true
	}
	long := []string{"áa", "éb", "íc", "ód", "úe", "àf", "èg", "ìh"}
	if got := topicKnowledgeQueries(long, strings.Join(long, " ")); len(got) > 12 {
		t.Fatalf("query bound exceeded: %d", len(got))
	}
	if got := topicKnowledgeQueries([]string{"atlas"}, "what about Atlas"); !slices.Equal(got, []string{"atlas"}) {
		t.Fatalf("plain questions must not change: %q", got)
	}
}
