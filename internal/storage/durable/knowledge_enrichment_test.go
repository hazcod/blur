package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
)

func enrichmentFixture(t *testing.T, s *Store, domain, text string, proposals ...derive.Proposal) enrich.Chunk {
	t.Helper()
	source, err := capture.SanitizeObservation("chat.app", domain, text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(context.Background(), source, time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	for i := range proposals {
		proposals[i].SourceID = source.ContentID()
	}
	return enrich.Chunk{SourceID: source.ContentID(), ModelKey: enrich.ModelKey("synthetic-local-model"), End: len(source.Text()), Next: len(source.Text()), Complete: true, Proposals: proposals}
}

func TestAutomaticLibraryPopularEntityDeletionFitsTransactionBudget(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xc4), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	for i := 0; i < knowledge.MaxFacts; i++ {
		text := fmt.Sprintf("Atlas captured fact %d.", i)
		chunk := enrichmentFixture(t, s, "work.example", text, derive.Proposal{Kind: knowledge.Project, Name: "Atlas", Fact: text})
		if err := s.ApplyEnrichment(ctx, chunk); err != nil {
			t.Fatal(i, err)
		}
	}
	id := enrich.EntityID(knowledge.Project, "Atlas", "chat.app", "work.example")
	if err := s.DeleteInferredKnowledge(ctx, id); err != nil {
		t.Fatalf("cannot remove full supported entity: %v", err)
	}
	if _, found, err := s.GetInferredKnowledge(ctx, id); err != nil || found {
		t.Fatal("removed entry retained", found, err)
	}
}

func TestAutomaticLibrarySourceFanoutDeletionFitsTransactionBudget(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xc5), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o, err := capture.SanitizeObservation("chat.app", "work.example", strings.Repeat("Many explicitly mentioned project names. ", 600), capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(ctx, o, time.Now()); err != nil {
		t.Fatal(err)
	}
	start, entityNumber := 0, 0
	for entityNumber < enrich.MaxSourceEntities {
		end, next, done, err := enrich.NextChunk(o.Text(), start)
		if err != nil || done {
			t.Fatal("fixture source too short", err)
		}
		chunk := enrich.Chunk{SourceID: o.ContentID(), ModelKey: enrich.ModelKey("fixture"), Start: start, End: end, Next: next}
		for i := 0; i < derive.MaxSuggestions && entityNumber < enrich.MaxSourceEntities; i++ {
			entityNumber++
			name := fmt.Sprintf("Project %d", entityNumber)
			chunk.Proposals = append(chunk.Proposals, derive.Proposal{Kind: knowledge.Project, Name: name, Fact: name + " is mentioned in this source.", SourceID: o.ContentID()})
		}
		if err := s.ApplyEnrichment(ctx, chunk); err != nil {
			t.Fatal(entityNumber, err)
		}
		start = next
	}
	if err := s.DeleteObservationRecord(ctx, o.ContentID()); err != nil {
		t.Fatalf("cannot delete source with full supported Library fanout: %v", err)
	}
	page, err := s.ListInferredKnowledge(ctx, enrich.PageRequest{Limit: 100})
	if err != nil || len(page.Entities) != 0 || page.HasMore {
		t.Fatalf("source deletion retained inferred data: %+v %v", page, err)
	}
}

func TestAutomaticLibraryContextsConnectionsAndContradictions(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xc1), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	a := enrichmentFixture(t, s, "one.example", "Alex maintains Atlas.", derive.Proposal{Kind: knowledge.Person, Name: "Alex", Fact: "Alex maintains Atlas."}, derive.Proposal{Kind: knowledge.Project, Name: "Atlas", Fact: "Atlas is maintained by Alex."})
	if err := s.ApplyEnrichment(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := enrichmentFixture(t, s, "two.example", "Alex maintains Borealis.", derive.Proposal{Kind: knowledge.Person, Name: "Alex", Fact: "Alex maintains Borealis."})
	if err := s.ApplyEnrichment(ctx, b); err != nil {
		t.Fatal(err)
	}
	people, err := s.FindInferredKnowledge(ctx, " ALEX ")
	if err != nil || len(people) != 2 || people[0].ID == people[1].ID {
		t.Fatalf("same-name people: %+v %v", people, err)
	}
	id := enrich.EntityID(knowledge.Person, "Alex", "chat.app", "one.example")
	person, found, err := s.GetInferredKnowledge(ctx, id)
	if err != nil || !found || person.State != knowledge.Proposed || len(person.RelatedIDs) != 1 || person.Facts[0].Sources[0].ID != a.SourceID {
		t.Fatalf("unconnected/unbacked entry: %+v %v", person, err)
	}
	if err := s.ApplyEnrichment(ctx, a); err != nil {
		t.Fatal("idempotent replay:", err)
	}
	c := enrichmentFixture(t, s, "one.example", "Atlas now has a different maintainer.", derive.Proposal{Kind: knowledge.Person, Name: "Alex", Fact: "Alex no longer maintains Atlas."})
	if err := s.ApplyEnrichment(ctx, c); err != nil {
		t.Fatal(err)
	}
	person, _, _ = s.GetInferredKnowledge(ctx, id)
	if len(person.Facts) != 2 {
		t.Fatalf("conflicting evidence was overwritten: %+v", person)
	}
	if err := s.DeleteObservationRecord(ctx, a.SourceID); err != nil {
		t.Fatal(err)
	}
	person, _, err = s.GetInferredKnowledge(ctx, id)
	if err != nil || len(person.Facts) != 1 || len(person.RelatedIDs) != 0 {
		t.Fatalf("deleted claims/links remained: %+v %v", person, err)
	}
	if err := s.ApplyEnrichment(ctx, a); !errors.Is(err, enrich.ErrSourceGone) {
		t.Fatalf("late job recreated deleted source: %v", err)
	}
}

func TestFindInferredKnowledgeByNamesBatchesAndDeduplicatesSubjects(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xc9), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	for _, fixture := range []struct{ domain, name string }{{"one.example", "Atlas"}, {"two.example", "Atlas"}, {"work.example", "Nova"}} {
		chunk := enrichmentFixture(t, s, fixture.domain, fixture.name+" is active.", derive.Proposal{Kind: knowledge.Project, Name: fixture.name, Fact: fixture.name + " is active."})
		if err := s.ApplyEnrichment(ctx, chunk); err != nil {
			t.Fatal(err)
		}
	}
	entities, more, err := s.FindInferredKnowledgeByNames(ctx, []string{"Atlas", "Nova", "Atlas"}, 3)
	if err != nil || more || len(entities) != 3 {
		t.Fatalf("batched Library lookup: len=%d more=%t err=%v", len(entities), more, err)
	}
	seen := map[string]bool{}
	for _, entity := range entities {
		if seen[entity.ID] || len(entity.Facts) == 0 {
			t.Fatalf("duplicate or stale Library entry: %+v", entity)
		}
		seen[entity.ID] = true
	}
	limited, more, err := s.FindInferredKnowledgeByNames(ctx, []string{"Atlas", "Nova"}, 2)
	if err != nil || !more || len(limited) != 2 {
		t.Fatalf("bounded Library lookup: len=%d more=%t err=%v", len(limited), more, err)
	}
}

func TestAutomaticLibraryCorrectionSurvivesReopenRebuildAndSourceDeletion(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xc2)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	chunk := enrichmentFixture(t, s, "private.example", "SecretProject is written in Ruby.", derive.Proposal{Kind: knowledge.Project, Name: "SecretProject", Fact: "SecretProject uses Ruby."})
	if err := s.ApplyEnrichment(ctx, chunk); err != nil {
		t.Fatal(err)
	}
	id := enrich.EntityID(knowledge.Project, "SecretProject", "chat.app", "private.example")
	factID := enrich.FactID(chunk.SourceID, chunk.Proposals[0].Fact)
	if err := s.CorrectInferredFact(ctx, id, factID, "SecretProject uses Go."); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SecretProject", "private.example", chunk.SourceID, "SecretProject uses Go."} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("plaintext Library data on disk")
		}
	}
	clear(raw)
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	chunk.ModelKey = enrich.ModelKey("replacement-local-model")
	if err := s.ApplyEnrichment(ctx, chunk); !errors.Is(err, enrich.ErrSuppressed) {
		t.Fatalf("rebuild overrode correction: %v", err)
	}
	entry, found, err := s.GetInferredKnowledge(ctx, id)
	if err != nil || !found || len(entry.Facts) != 1 || len(entry.Facts[0].Supersedes) != 1 || entry.Facts[0].Supersedes[0] != chunk.SourceID {
		t.Fatalf("correction lost superseded source provenance: %+v %v", entry, err)
	}
	if err := s.DeleteObservationRecord(ctx, chunk.SourceID); err != nil {
		t.Fatal(err)
	}
	entry, found, err = s.GetInferredKnowledge(ctx, id)
	if err != nil || !found || len(entry.Facts) != 1 || entry.Facts[0].Text != "SecretProject uses Go." || !entry.Facts[0].Sources[0].UserAuthored || len(entry.Facts[0].Supersedes) != 0 || len(entry.ActivitySourceIDs) != 0 {
		t.Fatalf("user correction/provenance: %+v %v", entry, err)
	}
	if _, found, err := s.GetEnrichmentProgress(ctx, chunk.SourceID); err != nil || found {
		t.Fatalf("deleted source progress remained: %v %v", found, err)
	}
	if err := s.DeleteAllKnowledge(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListInferredKnowledge(ctx, enrich.PageRequest{Limit: 10})
	if err != nil || len(page.Entities) != 0 || page.HasMore {
		t.Fatalf("delete-all retained corrections: %+v %v", page, err)
	}
}

func TestAutomaticLibraryRejectsInvalidAndStaleChunksAtomically(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xc3), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	chunk := enrichmentFixture(t, s, "work.example", strings.Repeat("Atlas context. ", 130))
	end, next, done, _ := enrich.NextChunk(strings.Repeat("Atlas context. ", 130), 0)
	chunk.End, chunk.Next, chunk.Complete = end, next, done
	chunk.Proposals = []derive.Proposal{{Kind: knowledge.Project, Name: "Atlas", Fact: "Atlas context.", SourceID: chunk.SourceID}}
	if err := s.ApplyEnrichment(ctx, chunk); err != nil {
		t.Fatal(err)
	}
	wrong := chunk
	wrong.Start = 1
	if err := s.ApplyEnrichment(ctx, wrong); !errors.Is(err, enrich.ErrStale) {
		t.Fatalf("stale chunk: %v", err)
	}
	progress, found, err := s.GetEnrichmentProgress(ctx, chunk.SourceID)
	if err != nil || !found || progress.Next != next || len(progress.Entities) != 1 {
		t.Fatalf("bad atomic progress: %+v %v", progress, err)
	}
	id := progress.Entities[0]
	if err := s.DismissInferredFact(ctx, id, enrich.FactID(chunk.SourceID, chunk.Proposals[0].Fact)); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyEnrichment(ctx, chunk); !errors.Is(err, enrich.ErrSuppressed) {
		t.Fatalf("dismissed fact returned: %v", err)
	}
	if err := s.DeleteInferredKnowledge(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetInferredKnowledge(ctx, id); err != nil || found {
		t.Fatalf("entry removal failed %v %v", found, err)
	}
	if err := s.ApplyEnrichment(ctx, chunk); !errors.Is(err, enrich.ErrSuppressed) {
		t.Fatalf("removed entry returned: %v", err)
	}
}
