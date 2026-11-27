package durable

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
)

type generationResetFixture struct {
	source      capture.SanitizedObservation
	entityIDs   []string
	oldKey      string
	chunk       enrich.Chunk
	confirmedID string
	obsoleteID  string
}

// Every source has enough spans to have named all 64 shared subjects. Records
// use normal <=512-byte facts and real immutable source identities. A confirmed
// and an obsolete proposed fact share the resetting source in every entity, so
// all 64 reverse receipts must survive even after obsolete claims are retired.
func seedGenerationResetFixture(t *testing.T, s *Store, entityCount, sourceCount int) generationResetFixture {
	t.Helper()
	ctx := context.Background()
	fixture := generationResetFixture{oldKey: enrich.ModelKey("prior-library-generator")}
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	sourceIDs := make([]string, sourceCount)
	for i := range sourceIDs {
		o, err := capture.SanitizeObservation("terminal.app", "", fmt.Sprintf("Historical source %03d. ", i)+strings.Repeat("Saved shared-project work evidence. ", 700), capture.MaxObservationTextBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, o, at.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
		sourceIDs[i] = o.ContentID()
		if i == 0 {
			fixture.source = o
		}
	}
	sort.Strings(sourceIDs)
	// Leave one source as an activity-only reference to make room for the
	// resetting source's second fact without exceeding the 128-fact format.
	omitted := sourceIDs[len(sourceIDs)-1]
	if omitted == fixture.source.ContentID() {
		omitted = sourceIDs[len(sourceIDs)-2]
	}
	makeRecord := func(name string, textBytes int) inferredRecord {
		id := enrich.EntityID(knowledge.Project, name, "terminal.app", "")
		record := inferredRecord{Entity: knowledge.Entity{ID: id, Kind: knowledge.Project, Name: name, State: knowledge.Proposed, Inference: &knowledge.Inference{AppID: "terminal.app"}, ActivitySourceIDs: slices.Clone(sourceIDs)}, Sources: slices.Clone(sourceIDs)}
		for j, sourceID := range sourceIDs {
			if sourceID == omitted {
				continue
			}
			text := fmt.Sprintf("%03d ", j) + strings.Repeat("x", textBytes-4)
			state := knowledge.Proposed
			if sourceID == fixture.source.ContentID() {
				state = knowledge.Confirmed
				fixture.confirmedID = enrich.FactID(sourceID, text)
			}
			record.Entity.Facts = append(record.Entity.Facts, knowledge.Fact{ID: enrich.FactID(sourceID, text), Text: text, State: state, Sources: []knowledge.Source{{ID: sourceID}}})
		}
		text := "Obsolete proposal. " + strings.Repeat("y", textBytes-len("Obsolete proposal. "))
		fixture.obsoleteID = enrich.FactID(fixture.source.ContentID(), text)
		record.Entity.Facts = append(record.Entity.Facts, knowledge.Fact{ID: fixture.obsoleteID, Text: text, State: knowledge.Proposed, Sources: []knowledge.Source{{ID: fixture.source.ContentID()}}})
		return record
	}
	// Binary-search the largest normal fact size admitted by the encrypted
	// record format once; equal-length names keep all 64 records the same size.
	low, high := len("Obsolete proposal. "), derive.MaxSuggestionBytes
	for low < high {
		middle := (low + high + 1) / 2
		if len(mustJSON(makeRecord("Shared Project 00", middle))) <= enrich.MaxEntityBytes {
			low = middle
		} else {
			high = middle - 1
		}
	}
	for i := range entityCount {
		record := makeRecord(fmt.Sprintf("Shared Project %02d", i), low)
		size := len(mustJSON(record))
		if sourceCount == knowledge.MaxFacts && size < enrich.MaxEntityBytes-128 || size > enrich.MaxEntityBytes || record.Entity.Validate() != nil {
			t.Fatalf("fixture must be valid and near full: bytes=%d", size)
		}
		if err := s.inferredUpdate(ctx, func(tx *Tx) error { return tx.putInferredRecord(record, false) }); err != nil {
			t.Fatal("seed entity", i, err)
		}
		fixture.entityIDs = append(fixture.entityIDs, record.Entity.ID)
	}
	sort.Strings(fixture.entityIDs)
	for _, sourceID := range sourceIDs {
		progress := enrich.Progress{SourceID: sourceID, ModelKey: fixture.oldKey, Next: len(fixture.source.Text()), Complete: true, ClaimsLimited: true, Entities: slices.Clone(fixture.entityIDs)}
		if err := s.inferredUpdate(ctx, func(tx *Tx) error { return tx.putEnrichmentProgress(progress) }); err != nil {
			t.Fatal(err)
		}
	}
	end, next, complete, err := enrich.NextSizedChunk(fixture.source.Text(), 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	fixture.chunk = enrich.Chunk{SourceID: fixture.source.ContentID(), ModelKey: enrich.ModelKey("new-compaction-generator"), SpanBytes: 4096, End: end, Next: next, Complete: complete, CompactionVersion: 1, Compaction: "Historical work summary.", Proposals: []derive.Proposal{{Kind: knowledge.Project, Name: "Shared Project 00", Fact: "Current generation accepted the revised rollout.", SourceID: fixture.source.ContentID()}}}
	return fixture
}

func assertGenerationResetClaims(t *testing.T, s *Store, fixture generationResetFixture, obsolete bool) {
	t.Helper()
	for _, id := range fixture.entityIDs {
		entity, found, err := s.GetInferredKnowledge(context.Background(), id)
		if err != nil || !found {
			t.Fatal("shared entity lost", found, err)
		}
		confirmed, proposed := false, false
		for _, fact := range entity.Facts {
			confirmed = confirmed || fact.ID == fixture.confirmedID && fact.State == knowledge.Confirmed
			proposed = proposed || fact.ID == fixture.obsoleteID
		}
		if !confirmed || proposed != obsolete || !slices.Contains(entity.ActivitySourceIDs, fixture.source.ContentID()) {
			t.Fatalf("generation reset changed confirmed/reverse claims: confirmed=%v proposed=%v", confirmed, proposed)
		}
	}
}

func TestCompactionGenerationResetRetiresFullSourceFanoutWithinTransactionBudget(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xf3), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	fixture := seedGenerationResetFixture(t, s, enrich.MaxSourceEntities, knowledge.MaxFacts)
	if err := s.ApplyEnrichment(ctx, fixture.chunk); err != nil {
		t.Fatal("full valid Library fanout stranded new source compaction", err)
	}
	progress, found, err := s.GetEnrichmentProgress(ctx, fixture.source.ContentID())
	if err != nil || !found || progress.ModelKey != fixture.chunk.ModelKey || progress.Next != fixture.chunk.Next || progress.CompactionParts != 1 || progress.ClaimsLimited || !slices.Equal(progress.Entities, fixture.entityIDs) {
		t.Fatal("reset did not publish exact new generation and confirmed receipts", progress, err)
	}
	assertGenerationResetClaims(t, s, fixture, false)
	entity, found, err := s.GetInferredKnowledge(ctx, enrich.EntityID(knowledge.Project, "Shared Project 00", "terminal.app", ""))
	if err != nil || !found || !slices.ContainsFunc(entity.Facts, func(f knowledge.Fact) bool { return f.Text == "Current generation accepted the revised rollout." }) {
		t.Fatal("generation reset dropped newly admitted proposals", entity, err)
	}
	if err := s.ApplyEnrichment(ctx, fixture.chunk); err != nil {
		t.Fatal("committed reset retry failed", err)
	}
	progress, _, err = s.GetEnrichmentProgress(ctx, fixture.source.ContentID())
	if err != nil || progress.CompactionParts != 1 {
		t.Fatal("reset retry duplicated source summary", progress, err)
	}
}

func TestCompactionGenerationResetInterruptedCleanupResumesAfterEncryptedReopen(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xf4)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	fixture := seedGenerationResetFixture(t, s, 3, 3)
	steps := 0
	_, err = s.retireEnrichmentGenerationClaims(ctx, fixture.chunk, func() error {
		steps++
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) || steps != 1 {
		t.Fatal("did not interrupt after a committed cleanup step", steps, err)
	}
	progress, found, err := s.GetEnrichmentProgress(ctx, fixture.source.ContentID())
	if err != nil || !found || progress.ModelKey != fixture.oldKey || progress.Next != len(fixture.source.Text()) || !progress.Complete || progress.CompactionParts != 0 || !slices.Equal(progress.Entities, fixture.entityIDs) {
		t.Fatal("partial cleanup changed generation/cursor or confirmed receipts", progress, err)
	}
	removed := 0
	for _, id := range fixture.entityIDs {
		entity, found, err := s.GetInferredKnowledge(ctx, id)
		if err != nil || !found {
			t.Fatal("partial cleanup lost shared entity", err)
		}
		if !slices.ContainsFunc(entity.Facts, func(f knowledge.Fact) bool { return f.ID == fixture.obsoleteID }) {
			removed++
		}
	}
	if removed != 1 {
		t.Fatal("interruption did not retain exactly the committed cleanup", removed)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyEnrichment(ctx, fixture.chunk); err != nil {
		t.Fatal("interrupted generation could not resume after reopen", err)
	}
	assertGenerationResetClaims(t, s, fixture, false)
	progress, _, err = s.GetEnrichmentProgress(ctx, fixture.source.ContentID())
	if err != nil || progress.ModelKey != fixture.chunk.ModelKey || progress.Next != fixture.chunk.Next || progress.CompactionParts != 1 {
		t.Fatal("resumed generation lost its new summary/cursor", progress, err)
	}
}

func TestCompactionGenerationResetDeletionBetweenCleanupStepsCannotRepublish(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xf5), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	fixture := seedGenerationResetFixture(t, s, 3, 3)
	steps := 0
	_, err = s.retireEnrichmentGenerationClaims(ctx, fixture.chunk, func() error {
		steps++
		return s.DeleteObservationRecord(ctx, fixture.source.ContentID())
	})
	if !errors.Is(err, enrich.ErrSourceGone) || steps != 1 {
		t.Fatal("cleanup survived source erasure", steps, err)
	}
	if err := s.ApplyEnrichment(ctx, fixture.chunk); !errors.Is(err, enrich.ErrSourceGone) {
		t.Fatal("late generation recreated erased source compaction", err)
	}
	if _, found, err := s.GetEnrichmentProgress(ctx, fixture.source.ContentID()); err != nil || found {
		t.Fatal("source erasure retained partial reset receipt", found, err)
	}
	for _, id := range fixture.entityIDs {
		entity, found, err := s.GetInferredKnowledge(ctx, id)
		if err != nil || !found {
			t.Fatal("erasure removed unrelated shared facts", found, err)
		}
		if slices.Contains(entity.ActivitySourceIDs, fixture.source.ContentID()) || slices.ContainsFunc(entity.Facts, func(f knowledge.Fact) bool {
			return slices.ContainsFunc(f.Sources, func(source knowledge.Source) bool { return source.ID == fixture.source.ContentID() })
		}) {
			t.Fatal("erasure retained source-backed confirmed/proposed claims", entity)
		}
	}
}

func TestCompactionGenerationResetFinalCommitRejectsChangedOldCursor(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xf6), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	fixture := seedGenerationResetFixture(t, s, 3, 3)
	const oldCursor = 21504
	if err := s.inferredUpdate(ctx, func(tx *Tx) error {
		progress, _, err := s.readEnrichmentProgress(tx.writer, fixture.source.ContentID())
		if err != nil {
			return err
		}
		progress.Next, progress.Complete = oldCursor, false
		return tx.putEnrichmentProgress(progress)
	}); err != nil {
		t.Fatal(err)
	}
	reset, err := s.retireEnrichmentGenerationClaims(ctx, fixture.chunk, nil)
	if err != nil || reset == nil {
		t.Fatal("generation cleanup failed", err)
	}
	end, next, complete, err := enrich.NextChunk(fixture.source.Text(), oldCursor)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyEnrichment(ctx, enrich.Chunk{SourceID: fixture.source.ContentID(), ModelKey: fixture.oldKey, Start: oldCursor, End: end, Next: next, Complete: complete}); err != nil {
		t.Fatal("concurrent old-model cursor did not advance", err)
	}
	if err := s.applyEnrichmentAfterReset(ctx, fixture.chunk, reset); !errors.Is(err, enrich.ErrStale) {
		t.Fatal("prepared generation overwrote a changed cursor", err)
	}
	progress, _, err := s.GetEnrichmentProgress(ctx, fixture.source.ContentID())
	if err != nil || progress.ModelKey != fixture.oldKey || progress.Next != next || progress.CompactionParts != 0 {
		t.Fatal("stale prepared reset changed committed progress", progress, err)
	}
}

func TestCompactionGenerationResetCorrectionDuringCleanupRemainsAuthoritative(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xf7), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	fixture := seedGenerationResetFixture(t, s, 3, 3)
	steps := 0
	_, err = s.retireEnrichmentGenerationClaims(ctx, fixture.chunk, func() error {
		steps++
		return s.CorrectInferredFact(ctx, fixture.entityIDs[0], fixture.confirmedID, "User correction remains authoritative.")
	})
	if !errors.Is(err, enrich.ErrStale) || steps != 1 {
		t.Fatal("cleanup ignored a user correction's suppression receipt", steps, err)
	}
	if err := s.ApplyEnrichment(ctx, fixture.chunk); err != nil {
		t.Fatal("suppressed claims stranded independent compaction", err)
	}
	entity, found, err := s.GetInferredKnowledge(ctx, fixture.entityIDs[0])
	if err != nil || !found || !slices.ContainsFunc(entity.Facts, func(f knowledge.Fact) bool {
		return f.ID == fixture.confirmedID && f.State == knowledge.Confirmed && f.Text == "User correction remains authoritative." && len(f.Sources) == 1 && f.Sources[0].UserAuthored
	}) {
		t.Fatal("new generation replaced the user's correction", entity, err)
	}
	progress, _, err := s.GetEnrichmentProgress(ctx, fixture.source.ContentID())
	if err != nil || !progress.Suppressed || progress.ModelKey != fixture.chunk.ModelKey || progress.CompactionParts != 1 {
		t.Fatal("source suppression and independent summary diverged", progress, err)
	}
}

func TestCompactionGenerationResetPreparedCommitCannotRecreateClearedLibrary(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xf8), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	fixture := seedGenerationResetFixture(t, s, 3, 3)
	reset, err := s.retireEnrichmentGenerationClaims(ctx, fixture.chunk, nil)
	if err != nil || reset == nil {
		t.Fatal("generation cleanup failed", err)
	}
	if err := s.DeleteAllKnowledge(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.applyEnrichmentAfterReset(ctx, fixture.chunk, reset); !errors.Is(err, enrich.ErrStale) {
		t.Fatal("prepared reset recreated a cleared Library receipt", err)
	}
	if _, found, err := s.GetEnrichmentProgress(ctx, fixture.source.ContentID()); err != nil || found {
		t.Fatal("cleared Library progress was republished", found, err)
	}
}
