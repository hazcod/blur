package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func sourceCompactionFixture(t *testing.T, s *Store, model string, size int) (string, []enrich.Chunk) {
	t.Helper()
	text := strings.Repeat("Private compaction source text remains complete. ", size/48+1)[:size]
	source, err := capture.SanitizeObservation("terminal.app", "private-compaction.example", text, capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(context.Background(), source, time.Now()); err != nil {
		t.Fatal(err)
	}
	var chunks []enrich.Chunk
	for start := 0; ; {
		end, next, complete, err := enrich.NextSizedChunk(text, start, 128)
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, enrich.Chunk{SourceID: source.ContentID(), ModelKey: enrich.ModelKey(model), SpanBytes: 128, Start: start, End: end, Next: next, Complete: complete, CompactionVersion: 1, Compaction: fmt.Sprintf("Private compaction block %d covers bytes %d through %d.", len(chunks), start, end)})
		if complete {
			break
		}
		start = next
	}
	return source.ContentID(), chunks
}

func applyCompactionChunks(t *testing.T, s *Store, chunks []enrich.Chunk) {
	t.Helper()
	for i, chunk := range chunks {
		if err := s.ApplyEnrichment(context.Background(), chunk); err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
	}
}

func TestSourceCompactionContinuesWhenLibraryCapacityIsReached(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xee), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	id, chunks := sourceCompactionFixture(t, s, "capacity-compaction", 3072)
	for i, chunk := range chunks {
		for j := range derive.MaxSuggestions {
			name := fmt.Sprintf("Project %03d", i*derive.MaxSuggestions+j)
			chunk.Proposals = append(chunk.Proposals, derive.Proposal{Kind: knowledge.Project, Name: name, Fact: name + " has recorded work.", SourceID: id})
		}
		if err := s.ApplyEnrichment(ctx, chunk); err != nil {
			t.Fatal("Library capacity stranded accepted summary text", i, err)
		}
	}
	p, found, err := s.GetEnrichmentProgress(ctx, id)
	if err != nil || !found || !p.Complete || !p.ClaimsLimited || p.CompactionParts != len(chunks) || len(p.Entities) > enrich.MaxSourceEntities {
		t.Fatalf("full source completion or explicit claim gap lost: %+v %v", p, err)
	}
	count := 0
	for after := 0; ; {
		page, err := s.ListSourceCompactions(ctx, id, after, 32)
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range page.Blocks {
			if block.Index != count || block.Text != chunks[count].Compaction {
				t.Fatal("capacity rollback dropped or duplicated a summary", count)
			}
			count++
		}
		if !page.HasMore {
			break
		}
		after = page.Next
	}
	if count != len(chunks) {
		t.Fatal("capacity limited source summaries", count, len(chunks))
	}
}

func TestSourceBackfillRetainsFactsPastOneEntityPage(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xef)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	modelKey := enrich.ModelKey("fact-page-capacity")
	for i := 0; i < knowledge.MaxFacts; i++ {
		fact := fmt.Sprintf("Atlas records accepted fact %03d.", i)
		chunk := enrichmentFixture(t, s, "work.example", fact, derive.Proposal{Kind: knowledge.Project, Name: "Atlas", Fact: fact})
		chunk.ModelKey = modelKey
		if err := s.ApplyEnrichment(ctx, chunk); err != nil {
			t.Fatal("seed supported fact", i, err)
		}
	}

	text := "Atlas records accepted fact 128."
	chunk := enrichmentFixture(t, s, "work.example", text, derive.Proposal{Kind: knowledge.Project, Name: "Atlas", Fact: text})
	chunk.ModelKey = modelKey
	chunk.CompactionVersion = 1
	chunk.Compaction = "The source records a new fact about Atlas."
	if err := s.ApplyEnrichment(ctx, chunk); err != nil {
		t.Fatal("accepted source was not durably processed", err)
	}

	id := enrich.EntityID(knowledge.Project, "Atlas", "chat.app", "work.example")
	entity, found, err := s.GetInferredKnowledge(ctx, id)
	if err != nil || !found || len(entity.Facts) != knowledge.MaxFacts || !entity.FactsHasMore || entity.FactsNext == "" {
		t.Fatalf("first bounded fact page or continuation is wrong: facts=%d more=%t next=%q found=%t err=%v", len(entity.Facts), entity.FactsHasMore, entity.FactsNext, found, err)
	}
	page, err := s.ListInferredFactsPage(ctx, id, knowledge.FactPageRequest{After: entity.FactsNext, Limit: 8})
	if err != nil || len(page.Facts) != 1 || page.Facts[0].Text != text || page.HasMore || page.Next != "" {
		t.Fatalf("continuation did not return the 129th accepted fact: %+v %v", page, err)
	}
	progress, found, err := s.GetEnrichmentProgress(ctx, chunk.SourceID)
	if err != nil || !found || !progress.Complete || progress.ClaimsLimited {
		t.Fatalf("accepted source still reports a structured capacity gap: %+v found=%t err=%v", progress, found, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	page, err = s.ListInferredFactsPage(ctx, id, knowledge.FactPageRequest{After: entity.FactsNext, Limit: 8})
	if err != nil || len(page.Facts) != 1 || page.Facts[0].Text != text || page.HasMore {
		t.Fatalf("fact continuation did not survive reopen: %+v %v", page, err)
	}
	factID := page.Facts[0].ID
	if err := s.ConfirmInferredFact(ctx, id, page.Facts[0]); err != nil {
		t.Fatal("overflow fact confirmation failed", err)
	}
	page, err = s.ListInferredFactsPage(ctx, id, knowledge.FactPageRequest{After: entity.FactsNext, Limit: 8})
	if err != nil || len(page.Facts) != 1 || page.Facts[0].ID != factID || page.Facts[0].State != knowledge.Confirmed {
		t.Fatalf("confirmed overflow fact was not visible: %+v %v", page, err)
	}
	if err := s.CorrectInferredFact(ctx, id, factID, "My corrected Atlas fact."); err != nil {
		t.Fatal("overflow fact correction failed", err)
	}
	page, err = s.ListInferredFactsPage(ctx, id, knowledge.FactPageRequest{After: entity.FactsNext, Limit: 8})
	if err != nil || len(page.Facts) != 1 || page.Facts[0].Text != "My corrected Atlas fact." || page.Facts[0].State != knowledge.Confirmed {
		t.Fatalf("corrected overflow fact was not visible: %+v %v", page, err)
	}
	if err := s.DeleteObservationRecord(ctx, chunk.SourceID); err != nil {
		t.Fatal("source deletion could not traverse overflow provenance", err)
	}
	page, err = s.ListInferredFactsPage(ctx, id, knowledge.FactPageRequest{After: entity.FactsNext, Limit: 8})
	if err != nil || len(page.Facts) != 1 || page.Facts[0].ID != factID || page.Facts[0].Text != "My corrected Atlas fact." || !page.Facts[0].Sources[0].UserAuthored || page.HasMore {
		t.Fatalf("source erasure did not preserve the detached user correction: %+v %v", page, err)
	}
	entity, found, err = s.GetInferredKnowledge(ctx, id)
	if err != nil || !found || !entity.FactsHasMore || entity.FactsNext == "" {
		t.Fatalf("source deletion lost the overflow continuation: more=%t next=%q found=%t err=%v", entity.FactsHasMore, entity.FactsNext, found, err)
	}
	page, err = s.ListInferredFactsPage(ctx, id, knowledge.FactPageRequest{After: entity.FactsNext, Limit: 8})
	if err != nil || !slices.ContainsFunc(page.Facts, func(f knowledge.Fact) bool { return f.ID == factID && f.Text == "My corrected Atlas fact." }) {
		t.Fatalf("source deletion discarded the user correction: %+v %v", page, err)
	}
	if err := s.DeleteInferredKnowledge(ctx, id); err != nil {
		t.Fatal("entity deletion failed to erase its fact/source pages", err)
	}
	if _, found, err := s.GetInferredKnowledge(ctx, id); err != nil || found {
		t.Fatalf("deleted entity remained: found=%t err=%v", found, err)
	}
}

func TestSourceCompactionPersistsAllSpansWithoutLifetimeBlockLimit(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xe1)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	id, chunks := sourceCompactionFixture(t, s, "source-block-model", 16000)
	if len(chunks) <= 64 {
		t.Fatal("fixture must verify more than 64 independent spans")
	}
	applyCompactionChunks(t, s, chunks)
	// A committed retry must not allocate another block.
	if err := s.ApplyEnrichment(ctx, chunks[len(chunks)-1]); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{id, "Private compaction block", "Private compaction source"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("plaintext secret persisted: %q", secret)
		}
	}
	clear(raw)
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	progress, found, err := s.GetEnrichmentProgress(ctx, id)
	if err != nil || !found || progress.CompactionParts != len(chunks) || progress.CompactionSlots != len(chunks) {
		t.Fatalf("incomplete durable block receipt: %+v, %v", progress, err)
	}
	var got []enrich.CompactionBlock
	for after := 0; ; {
		page, err := s.ListSourceCompactions(ctx, id, after, 7)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page.Blocks...)
		if !page.HasMore {
			break
		}
		if page.Next <= after {
			t.Fatal("pagination did not progress")
		}
		after = page.Next
	}
	if len(got) != len(chunks) {
		t.Fatalf("saved %d of %d spans", len(got), len(chunks))
	}
	for i, block := range got {
		want := chunks[i]
		if block.Index != i || block.SourceID != id || block.ModelKey != want.ModelKey || block.Start != want.Start || block.End != want.End || block.Next != want.Next || block.Text != want.Compaction {
			t.Fatalf("block %d lost exact source span: %+v", i, block)
		}
	}
}

func TestSourceCompactionModelResetHidesOldBlocksAndPreservesPriorClaims(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe2), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	id, chunks := sourceCompactionFixture(t, s, "old-block-model", 1200)
	chunks[0].Proposals = []derive.Proposal{{Kind: knowledge.Project, Name: "PrivateProject", Fact: "PrivateProject is active.", SourceID: id}}
	chunks[0].ClaimsLimited = true
	applyCompactionChunks(t, s, chunks)
	reset := chunks[0]
	reset.ModelKey, reset.Proposals, reset.PreserveClaims, reset.ClaimsLimited = enrich.ModelKey("new-block-model"), nil, true, false
	reset.Compaction = "Current generation first source span."
	if err := s.ApplyEnrichment(ctx, reset); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListSourceCompactions(ctx, id, 0, 32)
	if err != nil || page.HasMore || len(page.Blocks) != 1 || page.Blocks[0].Text != reset.Compaction || page.Blocks[0].ModelKey != reset.ModelKey {
		t.Fatalf("old generation leaked: %+v %v", page, err)
	}
	progress, _, err := s.GetEnrichmentProgress(ctx, id)
	if err != nil || progress.CompactionParts != 1 || progress.CompactionSlots != len(chunks) || len(progress.Entities) != 1 || !progress.ClaimsLimited {
		t.Fatalf("reset lost high-water erasure or claims: %+v %v", progress, err)
	}
	entity, found, err := s.GetInferredKnowledge(ctx, progress.Entities[0])
	if err != nil || !found || len(entity.Facts) != 1 {
		t.Fatalf("summary-only rebuild removed facts: %+v %v", entity, err)
	}
	if err := s.DeleteObservationRecord(ctx, id); err != nil {
		t.Fatal(err)
	}
	assertCompactionSlotsAbsent(t, s, id, len(chunks))
}

func assertCompactionSlotsAbsent(t *testing.T, s *Store, id string, slots int) {
	t.Helper()
	if err := s.view(context.Background(), func(reader bolt.Reader) error {
		for i := 0; i < slots; i++ {
			_, found, err := s.readIndexValueReader(reader, sourceCompactionNS, sourceCompactionID(id, i), maxSourceCompactionRecordBytes)
			if err != nil {
				return err
			}
			if found {
				return fmt.Errorf("retained compaction slot %d", i)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceCompactionInterruptedErasureRecoversBeforePublishing(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xe3)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	id, chunks := sourceCompactionFixture(t, s, "delete-block-model", 9000)
	applyCompactionChunks(t, s, chunks)
	steps := 0
	err = s.deleteSourceStaged(ctx, id, func() error {
		steps++
		if steps == 15 {
			return context.Canceled
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("did not interrupt deletion: %v", err)
	}
	if _, err := s.ListSourceCompactions(ctx, id, 0, 32); !errors.Is(err, ErrDeletionPending) {
		t.Fatalf("partial erased source readable: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	assertCompactionSlotsAbsent(t, s, id, len(chunks))
	if page, err := s.ListSourceCompactions(ctx, id, 0, 32); err != nil || len(page.Blocks) != 0 {
		t.Fatalf("erased blocks visible after recovery: %+v %v", page, err)
	}
}

func TestSourceCompactionDeleteAllKnowledgeErasesEverySlot(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe4), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	id, chunks := sourceCompactionFixture(t, s, "purge-block-model", 9000)
	applyCompactionChunks(t, s, chunks)
	if err := s.DeleteAllKnowledge(ctx); err != nil {
		t.Fatal(err)
	}
	assertCompactionSlotsAbsent(t, s, id, len(chunks))
	if page, err := s.ListSourceCompactions(ctx, id, 0, 32); err != nil || len(page.Blocks) != 0 {
		t.Fatalf("purged blocks visible: %+v %v", page, err)
	}
	if _, found, err := s.GetObservation(ctx, id); err != nil || !found {
		t.Fatalf("knowledge purge removed original: %t %v", found, err)
	}
}

func TestSourceCompactionPagesByBytesAndRejectsInvalidCursors(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe5), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	id, chunks := sourceCompactionFixture(t, s, "page-block-model", 1024)
	for i := range chunks {
		// HTML escaping expands each source character to six serialized bytes.
		// Even maximum valid individual blocks must remain fully pageable.
		chunks[i].Compaction = strings.Repeat("<", derive.MaxEvidenceItemBytes)
	}
	applyCompactionChunks(t, s, chunks)
	page, err := s.ListSourceCompactions(ctx, id, 0, 32)
	if err != nil || !page.HasMore || len(page.Blocks) == 0 || len(page.Blocks) >= len(chunks) {
		t.Fatalf("byte budget did not preserve continuation: %+v %v", page, err)
	}
	for count, after := len(page.Blocks), page.Next; ; {
		page, err = s.ListSourceCompactions(ctx, id, after, 32)
		if err != nil {
			t.Fatal(err)
		}
		count += len(page.Blocks)
		if !page.HasMore {
			if count != len(chunks) {
				t.Fatalf("byte-paged tail missing: %d of %d", count, len(chunks))
			}
			break
		}
		if page.Next <= after {
			t.Fatal("byte-paged cursor stalled")
		}
		after = page.Next
	}
	for _, request := range []struct{ after, limit int }{{-1, 1}, {0, 0}, {0, 33}} {
		if _, err := s.ListSourceCompactions(ctx, id, request.after, request.limit); !errors.Is(err, enrich.ErrInvalid) {
			t.Fatalf("invalid page accepted: %+v %v", request, err)
		}
	}
	if _, err := s.ListSourceCompactions(ctx, id, len(chunks)+1, 1); !errors.Is(err, enrich.ErrStale) {
		t.Fatalf("stale generation cursor accepted: %v", err)
	}
}

func TestSourceCompactionPublishedPartErasureRemovesBlocks(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe6), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	summary := publishIndexedFixture(t, s, "Private published source compaction.", time.Now())
	drainCaptureIndex(t, s)
	var id string
	if err := s.view(ctx, func(reader bolt.Reader) error {
		edition, _, found, err := s.captureEditionReader(reader, summary.ContentID())
		if err != nil || !found {
			return ErrCorrupt
		}
		id = capturePartSourceID(summary.ContentID(), edition.Publication, 0)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var source capture.Evidence
	var found bool
	if err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		source, _, found, err = s.sourceEvidenceReader(reader, id)
		return err
	}); err != nil || !found {
		t.Fatalf("published part missing: %t %v", found, err)
	}
	chunk := enrich.Chunk{SourceID: id, ModelKey: enrich.ModelKey("part-block-model"), End: len(source.Text()), Next: len(source.Text()), Complete: true, CompactionVersion: 1, Compaction: "Private published span summary."}
	if err := s.ApplyEnrichment(ctx, chunk); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteCapturesBefore(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	assertCompactionSlotsAbsent(t, s, id, 1)
	if err := s.ApplyEnrichment(ctx, chunk); !errors.Is(err, enrich.ErrSourceGone) {
		t.Fatalf("late summary recreated erased part: %v", err)
	}
}
