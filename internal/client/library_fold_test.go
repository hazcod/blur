package client

import (
	"context"
	"fmt"
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

// libraryNameFixture stores one inferred Library entity per name through the
// same source-validated path as background enrichment.
func libraryNameFixture(t *testing.T, names ...string) *Client {
	t.Helper()
	entries := make([]libraryEntry, len(names))
	for i, name := range names {
		entries[i] = libraryEntry{knowledge.Person, name}
	}
	return libraryEntryFixture(t, entries...)
}

type libraryEntry struct {
	kind knowledge.Kind
	name string
}

// libraryEntryFixture is libraryNameFixture with a chosen kind per entry.
func libraryEntryFixture(t *testing.T, entries ...libraryEntry) *Client {
	t.Helper()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c := NewClient(s)
	t.Cleanup(c.Close)
	for _, entry := range entries {
		name := entry.name
		text := name + " owns the vendor contract renewal."
		source, err := capture.SanitizeObservation("editor.app", "work.example", text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(context.Background(), source, time.Now()); err != nil {
			t.Fatal(err)
		}
		chunk := enrich.Chunk{SourceID: source.ContentID(), ModelKey: enrich.ModelKey("fixture"), End: len(text), Next: len(text), Complete: true,
			Proposals: []derive.Proposal{{Kind: entry.kind, Name: name, Fact: text, SourceID: source.ContentID()}}}
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
	for _, question := range []string{"What did we decide about José Müller?", "what did we decide about JOSÉ MÜLLER", "What did we decide about jose muller?", "What did we decide about José Müller?"} {
		if facts := libraryFacts(t, c, question); !hasFact(facts, "José Müller owns") {
			t.Fatalf("%q lost the accented Library entity: %v", question, facts)
		}
	}
}

func TestEntityQuestionFindsAccentedLibraryEntityFromAsciiName(t *testing.T) {
	c := libraryNameFixture(t, "José")
	plan, err := assistant.PlanQuestion("What do you know about Jose?", time.Now(), nil)
	if err != nil || plan.Entity == "" {
		t.Fatalf("plan: %+v err=%v", plan, err)
	}
	evidence, err := c.inferredEntityEvidence(context.Background(), plan)
	if err != nil || !hasFact(evidenceTexts(evidence), "José owns") {
		t.Fatalf("entity lookup missed the accented Library entity: %+v err=%v", evidence, err)
	}
}

func TestFoldedLibrarySubjectReportsOmittedContexts(t *testing.T) {
	c := libraryNameFixture(t)
	s := c.data.(*durable.Store)
	ctx := context.Background()
	for index := 0; index < 129; index++ {
		name := "José"
		if index%2 == 0 {
			name = "Jose"
		}
		text := fmt.Sprintf("%s owns supplier renewal %d.", name, index)
		o, err := capture.SanitizeObservation("editor.app", fmt.Sprintf("work%d.example", index), text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, o, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := s.ApplyEnrichment(ctx, enrich.Chunk{SourceID: o.ContentID(), ModelKey: enrich.ModelKey("fixture"), End: len(text), Next: len(text), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Person, Name: name, Fact: text, SourceID: o.ContentID()}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, question := range []string{"What do you know about Jose?", "What's outstanding for Jose?"} {
		plan, err := assistant.PlanQuestion(question, time.Now(), nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.RetrievePlan(ctx, plan)
		if err != nil || !got.Diagnosis.LibraryRecallLimited {
			t.Fatalf("%q hid omitted contexts or failed: diagnosis=%+v err=%v", question, got.Diagnosis, err)
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
	if err != nil || !hasFact(evidenceTexts(evidence), "Jose owns") {
		t.Fatalf("entity lookup missed the plain-spelling Library entity: %+v err=%v", evidence, err)
	}
}

func evidenceTexts(evidence []assistant.Evidence) []string {
	texts := make([]string, 0, len(evidence))
	for _, item := range evidence {
		texts = append(texts, item.Text)
	}
	return texts
}

// plannedTopicQueries runs the real planner so the terms carry its possessive
// and fold handling, then builds the Library query set from them.
func plannedTopicQueries(t *testing.T, question string) []string {
	t.Helper()
	plan, err := assistant.PlanQuestion(question, time.Now(), nil)
	if err != nil || len(plan.Terms) == 0 {
		t.Fatalf("plan %q: %+v err=%v", question, plan, err)
	}
	return topicKnowledgeQueries(plan.Terms, plan.Question)
}

func TestPossessiveQuestionFindsStoredLibraryName(t *testing.T) {
	c := libraryNameFixture(t, "José")
	if facts := libraryFacts(t, c, "what do I know about José's team"); !hasFact(facts, "José owns") {
		t.Fatalf("possessive accented question lost the stored entity: %v", facts)
	}
	for _, question := range []string{"what do I know about José's team", "what do I know about José\u2019s team"} {
		queries := plannedTopicQueries(t, question)
		if !slices.Contains(queries, "josé") || slices.ContainsFunc(queries, func(q string) bool { return strings.Contains(q, "'s") || strings.Contains(q, "\u2019s") }) {
			t.Fatalf("%q: possessive not stripped from the original spelling: %q", question, queries)
		}
	}
}

func TestPossessiveAsciiQuestionAddsNoExtraQueries(t *testing.T) {
	plain := plannedTopicQueries(t, "what do I know about Atlas launch")
	for _, question := range []string{"what do I know about Atlas's launch", "what do I know about Atlas\u2019s launch"} {
		if possessive := plannedTopicQueries(t, question); !slices.Equal(possessive, plain) {
			t.Fatalf("%q changed the query set: %q vs %q", question, possessive, plain)
		}
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

func TestFoldedLibraryScopeAppliedBeforeCandidateLimit(t *testing.T) {
	c := libraryNameFixture(t)
	s := c.data.(*durable.Store)
	ctx := context.Background()
	for i := 0; i < 32; i++ {
		text := fmt.Sprintf("José owns renewal %d.", i)
		o, err := capture.SanitizeObservation("editor.app", fmt.Sprintf("work%d.example", i), text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, o, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := s.ApplyEnrichment(ctx, enrich.Chunk{SourceID: o.ContentID(), ModelKey: enrich.ModelKey("fixture"), End: len(text), Next: len(text), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Person, Name: "José", Fact: text, SourceID: o.ContentID()}}}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.FindInferredKnowledge(ctx, "José")
	if err != nil || len(all) != 32 {
		t.Fatal("fixture", len(all), err)
	}
	target := all[len(all)-1]
	const correction = "José coordinates the exclusive scoped exception."
	if err := s.CorrectInferredFact(ctx, target.ID, target.Facts[0].ID, correction); err != nil {
		t.Fatal(err)
	}
	plan := assistant.Plan{Kind: assistant.KindEntity, Entity: "Jose", Question: "What do you know about Jose?", Domains: []string{target.Inference.Domain}}
	got, err := c.RetrievePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFact(evidenceTexts(got.Evidence), correction) {
		t.Fatalf("sole requested domain %s lost its authored correction among 32 contexts; diagnosis=%+v evidence=%v", target.Inference.Domain, got.Diagnosis, evidenceTexts(got.Evidence))
	}
}
