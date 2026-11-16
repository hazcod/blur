package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
)

func TestEnrichmentDeclinedReceiptIsAtomicTerminalAndErasedWithSource(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xda)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	chunk := enrichmentFixture(t, s, "work.example", strings.Repeat("Atlas has an offline export workflow. ", 70))
	text, found, err := s.GetObservation(ctx, chunk.SourceID)
	if err != nil || !found {
		t.Fatal(err)
	}
	chunk.End, chunk.Next, chunk.Complete, err = enrich.NextChunk(text.Text(), 0)
	if err != nil {
		t.Fatal(err)
	}
	chunk.Proposals = []derive.Proposal{{Kind: knowledge.Project, Name: "Atlas", Fact: "Atlas has an offline export workflow.", SourceID: chunk.SourceID}}
	if err := s.ApplyEnrichment(ctx, chunk); err != nil {
		t.Fatal(err)
	}
	declined := enrich.Chunk{SourceID: chunk.SourceID, ModelKey: chunk.ModelKey, Start: chunk.Next, Declined: true}
	declined.End, declined.Next, declined.Complete, err = enrich.NextChunk(text.Text(), declined.Start)
	if err != nil {
		t.Fatal(err)
	}
	for _, malformed := range []enrich.Chunk{
		{SourceID: chunk.SourceID, ModelKey: chunk.ModelKey, Start: declined.Start, End: declined.End, Next: declined.Next + 1, Complete: declined.Complete, Declined: true},
		{SourceID: chunk.SourceID, ModelKey: chunk.ModelKey, Start: declined.Start, End: declined.End, Next: declined.Next, Complete: declined.Complete, Declined: true, Proposals: chunk.Proposals},
	} {
		if err := s.ApplyEnrichment(ctx, malformed); err == nil {
			t.Fatal("declined marker authorized an invalid span or proposals")
		}
	}
	for range 2 {
		if err := s.ApplyEnrichment(ctx, declined); err != nil {
			t.Fatal("terminal receipt retry", err)
		}
	}
	progress, found, err := s.GetEnrichmentProgress(ctx, chunk.SourceID)
	if err != nil || !found || !progress.Declined || progress.Complete || progress.Next != declined.Start || len(progress.Entities) != 1 {
		t.Fatal("declined outcome changed covered prefix", err)
	}
	normal := declined
	normal.Declined = false
	if err := s.ApplyEnrichment(ctx, normal); !errors.Is(err, enrich.ErrStale) {
		t.Fatal("same-model normal retry overrode terminal refusal", err)
	}
	if err := s.ApplyEnrichment(ctx, chunk); !errors.Is(err, enrich.ErrStale) {
		t.Fatal("old committed prefix erased terminal refusal", err)
	}
	if err := s.PutEnrichmentCheckpoint(ctx, enrich.Checkpoint{ModelKey: chunk.ModelKey, Complete: true, CoverageLimited: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("Atlas")) || bytes.Contains(raw, []byte(chunk.SourceID)) || bytes.Contains(raw, []byte("declined")) {
		t.Fatal("refusal receipt or source was plaintext on disk")
	}
	clear(raw)
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	if progress, found, err := s.GetEnrichmentProgress(ctx, chunk.SourceID); err != nil || !found || !progress.Declined || progress.Next != declined.Start {
		t.Fatal("terminal receipt did not survive encrypted reopen", err)
	}
	if err := s.DeleteObservationRecord(ctx, chunk.SourceID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetEnrichmentProgress(ctx, chunk.SourceID); err != nil || found {
		t.Fatal("source erasure retained terminal receipt", err)
	}
	if page, err := s.ListInferredKnowledge(ctx, enrich.PageRequest{Limit: 10}); err != nil || len(page.Entities) != 0 {
		t.Fatal("source erasure retained partial claims", err)
	}
	if checkpoint, _, err := s.GetEnrichmentCheckpoint(ctx); err != nil || checkpoint.Complete || checkpoint.CoverageLimited {
		t.Fatal("source erasure retained stale coverage checkpoint", err)
	}
	if err := s.ApplyEnrichment(ctx, declined); !errors.Is(err, enrich.ErrSourceGone) {
		t.Fatal("late refusal recreated erased evidence", err)
	}
}

func TestEnrichmentOptionalOutcomeFieldsPreserveLegacyCanonicalBytes(t *testing.T) {
	id, key := strings.Repeat("a", 64), strings.Repeat("b", 64)
	progress := enrich.Progress{SourceID: id, ModelKey: key, Next: 896}
	want := `{"source_id":"` + id + `","model_key":"` + key + `","next":896,"complete":false}`
	if got := string(mustJSON(progress)); got != want {
		t.Fatal("zero optional receipt field changed authenticated legacy bytes")
	}
	checkpoint := enrich.Checkpoint{ModelKey: key, Complete: true}
	want = `{"model_key":"` + key + `","complete":true}`
	if got := string(mustJSON(checkpoint)); got != want {
		t.Fatal("zero optional checkpoint field changed authenticated legacy bytes")
	}
	var old enrich.Progress
	if err := json.Unmarshal(mustJSON(progress), &old); err != nil || !enrich.ValidProgress(old) || old.Declined {
		t.Fatal("legacy progress no longer decodes", err)
	}
	progress.Declined, progress.Complete = true, true
	if enrich.ValidProgress(progress) {
		t.Fatal("declined receipt claimed full coverage")
	}
}

func TestEnrichmentCheckpointRetainsIndependentBoundedCaptureCursor(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xdc), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	chunk := enrichmentFixture(t, s, "work.example", "Synthetic legacy cursor source.")
	checkpoint := enrich.Checkpoint{After: chunk.SourceID, ModelKey: chunk.ModelKey, Complete: true, CaptureAfter: strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64) + ":9223372036854775807", CaptureComplete: false, CoverageLimited: true}
	if len(mustJSON(checkpoint)) > 512 {
		t.Fatal("appended checkpoint fields exceeded authenticated envelope limit")
	}
	if err := s.PutEnrichmentCheckpoint(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetEnrichmentCheckpoint(ctx)
	if err != nil || !found || got != checkpoint {
		t.Fatal("independent capture cursor was lost or changed legacy progress", err)
	}
	for _, cursor := range []string{strings.Repeat("x", 151), strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64) + ":-1", strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64) + ":01"} {
		checkpoint.CaptureAfter = cursor
		if err := s.PutEnrichmentCheckpoint(ctx, checkpoint); !errors.Is(err, enrich.ErrInvalid) {
			t.Fatal("invalid capture cursor accepted", err)
		}
	}
}
