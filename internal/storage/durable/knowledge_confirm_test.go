package durable

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
)

func TestInferredFactConfirmationPreservesAttributionAcrossReindexAndErasure(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xd3)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	chunk := enrichmentFixture(t, s, "work.example", "Atlas ships Friday. Atlas uses Go.", derive.Proposal{Kind: knowledge.Project, Name: "Atlas", Fact: "Atlas ships Friday."})
	if err := s.ApplyEnrichment(ctx, chunk); err != nil {
		t.Fatal(err)
	}
	id := enrich.EntityID(knowledge.Project, "Atlas", "chat.app", "work.example")
	entity, found, err := s.GetInferredKnowledge(ctx, id)
	if err != nil || !found || len(entity.Facts) != 1 {
		t.Fatal(entity, found, err)
	}
	expected := entity.Facts[0]
	wrong := expected
	wrong.Text = "Atlas ships Monday."
	if err := s.ConfirmInferredFact(ctx, id, wrong); !errors.Is(err, enrich.ErrStale) {
		t.Fatal("confirmed changed preview", err)
	}
	if err := s.ConfirmInferredFact(ctx, id, expected); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmInferredFact(ctx, id, expected); err != nil {
		t.Fatal("retry", err)
	}
	// The next model no longer emits the confirmed fact; its new facts must
	// still be admitted without removing the explicit confirmation.
	chunk.ModelKey = enrich.ModelKey("replacement-model")
	chunk.Proposals[0].Fact = "Atlas uses Go."
	if err := s.ApplyEnrichment(ctx, chunk); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	entity, found, err = s.GetInferredKnowledge(ctx, id)
	if err != nil || !found || len(entity.Facts) != 2 {
		t.Fatal("confirmation or new inference lost", entity, found, err)
	}
	matched := false
	for _, fact := range entity.Facts {
		if fact.ID == expected.ID {
			matched = true
			if fact.State != knowledge.Confirmed || fact.Text != expected.Text || !slices.Equal(fact.Sources, expected.Sources) {
				t.Fatal("confirmation rewrote attribution", fact)
			}
		}
	}
	if !matched {
		t.Fatal("confirmed fact absent")
	}
	chunk.ModelKey, chunk.Proposals = enrich.ModelKey("model-with-no-proposals"), nil
	if err := s.ApplyEnrichment(ctx, chunk); err != nil {
		t.Fatal(err)
	}
	entity, found, err = s.GetInferredKnowledge(ctx, id)
	if err != nil || !found || len(entity.Facts) != 1 || entity.Facts[0].ID != expected.ID || entity.Facts[0].State != knowledge.Confirmed {
		t.Fatal("empty regeneration removed confirmation", entity, found, err)
	}
	if err := s.DeleteObservationRecord(ctx, chunk.SourceID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetInferredKnowledge(ctx, id); err != nil || found {
		t.Fatal("confirmed source-backed fact escaped erasure", found, err)
	}
	if err := s.ApplyEnrichment(ctx, chunk); !errors.Is(err, enrich.ErrSourceGone) {
		t.Fatal("late regeneration", err)
	}
}
