package durable

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func TestEnrichmentConsumesPublishedPartIdentityAndErasesItsProjections(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xdb), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	summary := publishIndexedFixture(t, s, "Project Atlas uses an offline export workflow.", time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC))
	drainCaptureIndex(t, s)
	var sourceID string
	if err := s.view(ctx, func(r bolt.Reader) error {
		edition, _, found, err := s.captureEditionReader(r, summary.ContentID())
		if err != nil || !found {
			return ErrCorrupt
		}
		sourceID = capturePartSourceID(summary.ContentID(), edition.Publication, 0)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetObservation(ctx, sourceID); err != nil || found {
		t.Fatal("multipart source forged a legacy observation alias", err)
	}
	page, err := s.ReadCaptureParts(ctx, summary.ContentID(), -1, 1)
	if err != nil || len(page.Parts) != 1 {
		t.Fatal(err)
	}
	text := page.Parts[0].Text()
	chunk := enrich.Chunk{SourceID: sourceID, ModelKey: enrich.ModelKey("synthetic-part-model"), End: len(text), Next: len(text), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Project, Name: "Atlas", Fact: "Atlas uses an offline export workflow.", SourceID: sourceID}}}
	if err := s.ApplyEnrichment(ctx, chunk); err != nil {
		t.Fatal("published immutable source part was not consumable by Library", err)
	}
	progress, found, err := s.GetEnrichmentProgress(ctx, sourceID)
	if err != nil || !found || !progress.Complete || len(progress.Entities) != 1 {
		t.Fatal("part progress missing", err)
	}
	entity, found, err := s.GetInferredKnowledge(ctx, progress.Entities[0])
	if err != nil || !found || len(entity.Facts) != 1 || entity.Facts[0].Sources[0].ID != sourceID {
		t.Fatal("part provenance was relabeled", err)
	}
	if _, err := s.DeleteCapturesBefore(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetEnrichmentProgress(ctx, sourceID); err != nil || found {
		t.Fatal("capture erasure retained part progress", err)
	}
	if _, found, err := s.GetInferredKnowledge(ctx, progress.Entities[0]); err != nil || found {
		t.Fatal("capture erasure retained part-derived claims", err)
	}
	if err := s.ApplyEnrichment(ctx, chunk); !errors.Is(err, enrich.ErrSourceGone) {
		t.Fatal("late part inference resurrected erased evidence", err)
	}
}
