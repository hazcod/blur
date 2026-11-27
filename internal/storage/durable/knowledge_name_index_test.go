package durable

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func TestInferredNameLookupFoldsBothSidesWithoutMergingContexts(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0x71)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ domain, name string }{{"one.example", "José Müller"}, {"two.example", "José Müller"}, {"three.example", "Jose Muller"}} {
		text := fixture.name + " owns the vendor contract renewal."
		chunk := enrichmentFixture(t, s, fixture.domain, text, derive.Proposal{Kind: knowledge.Person, Name: fixture.name, Fact: text})
		if err := s.ApplyEnrichment(ctx, chunk); err != nil {
			t.Fatal(err)
		}
	}
	if pending, err := s.LibraryNameIndexPending(ctx); err != nil || pending {
		t.Fatal("current writes scheduled needless name migration", pending, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, query := range []string{"jose muller", "JOSÉ MÜLLER", "José Müller", "  jose   muller "} {
		got, err := s.FindInferredKnowledge(ctx, query)
		if err != nil || len(got) != 3 {
			t.Fatalf("%q: got %d contexts, err=%v", query, len(got), err)
		}
		var domains []string
		for _, entity := range got {
			domains = append(domains, entity.Inference.Domain)
		}
		slices.Sort(domains)
		if !slices.Equal(domains, []string{"one.example", "three.example", "two.example"}) {
			t.Fatalf("contexts collapsed: %v", domains)
		}
	}
	rows, more, err := s.FindInferredKnowledgeByNames(ctx, []string{"jose muller", "José Müller"}, 2)
	if err != nil || len(rows) != 2 || !more {
		t.Fatalf("bounded lookup: rows=%d more=%v err=%v", len(rows), more, err)
	}
	for _, row := range rows {
		if err := s.DeleteInferredKnowledge(ctx, row.ID); err != nil {
			t.Fatal(err)
		}
	}
	remaining, err := s.FindInferredKnowledge(ctx, "jose muller")
	if err != nil || len(remaining) != 1 {
		t.Fatalf("deletion retained folded names: %d %v", len(remaining), err)
	}
	if err := s.DeleteObservationRecord(ctx, remaining[0].Facts[0].Sources[0].ID); err != nil {
		t.Fatal(err)
	}
	remaining, err = s.FindInferredKnowledge(ctx, "jose muller")
	if err != nil || len(remaining) != 0 {
		t.Fatalf("erased source retained folded match: %v %v", remaining, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"José Müller", "jose muller", "José Müller", "vendor contract"} {
		if bytesContains(data, []byte(plain)) {
			t.Fatalf("plaintext in store: %q", plain)
		}
	}
}

func TestLegacyInferredNamesBackfillResumesWithoutResurrectingDeletion(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0x72)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	var entities []knowledge.Entity
	for _, name := range []string{"José", "Chloé", "Müller"} {
		text := name + " owns the supplier renewal."
		chunk := enrichmentFixture(t, s, "work.example", text, derive.Proposal{Kind: knowledge.Person, Name: name, Fact: text})
		if err := s.ApplyEnrichment(ctx, chunk); err != nil {
			t.Fatal(err)
		}
		found, err := s.FindInferredKnowledge(ctx, name)
		if err != nil || len(found) != 1 {
			t.Fatal(found, err)
		}
		entities = append(entities, found[0])
	}
	// Restore precisely the prior layout: primary entities, exact names and
	// source receipts remain, but neither folded postings nor a cursor exist.
	if err := s.inferredUpdate(ctx, func(tx *Tx) error {
		for _, entity := range entities {
			if err := tx.deleteNamePostings(entity); err != nil {
				return err
			}
		}
		if err := tx.deleteInferredIndex(inferredMetaNS, foldedRetiredID); err != nil {
			return err
		}
		return tx.deleteInferredIndex(inferredMetaNS, nameTokenCursorID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	after, err := os.ReadFile(path)
	if err != nil || !slices.Equal(before, after) {
		t.Fatal("opening legacy names rewrote the store", err)
	}
	if pending, err := s.LibraryNameIndexPending(ctx); err != nil || !pending {
		t.Fatal("missing pending backfill", pending, err)
	}
	if old, err := s.FindInferredKnowledge(ctx, "José"); err != nil || len(old) != 1 {
		t.Fatal("legacy exact lookup lost", old, err)
	}
	if more, err := s.IndexPendingLibraryNames(ctx, 1); err != nil || !more {
		t.Fatal("backfill was not bounded to one entity", more, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := s.LibraryNameIndexPending(ctx); err != nil || !pending {
		t.Fatal("restart lost pending work", pending, err)
	}
	if err := s.DeleteInferredKnowledge(ctx, entities[1].ID); err != nil {
		t.Fatal(err)
	}
	if more, err := s.IndexPendingLibraryNames(ctx, 32); err != nil || more {
		t.Fatal("backfill failed to drain", more, err)
	}
	for _, fixture := range []struct {
		name  string
		count int
	}{{"jose", 1}, {"chloe", 0}, {"muller", 1}} {
		rows, err := s.FindInferredKnowledge(ctx, fixture.name)
		if err != nil || len(rows) != fixture.count {
			t.Fatalf("after restart %q: %d %v", fixture.name, len(rows), err)
		}
	}
	if err := s.DeleteAllKnowledge(ctx); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.FindInferredKnowledge(ctx, "jose"); err != nil || len(rows) != 0 {
		t.Fatal("delete-all retained folded index", rows, err)
	}
	for _, batches := range []int{0, 33} {
		if _, err := s.IndexPendingLibraryNames(ctx, batches); err != enrich.ErrInvalid {
			t.Fatalf("batch bound accepted %d: %v", batches, err)
		}
	}
}

func TestInferredNameLookupReservesPostingReadBudget(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x76), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for i := 0; i < enrich.MaxNameEntries; i++ {
		domain := fmt.Sprintf("workspace-%03d.example", i)
		for source := 0; source < 6; source++ {
			text := fmt.Sprintf("José owns contract %d in workspace %d.", source, i)
			chunk := enrichmentFixture(t, s, domain, text, derive.Proposal{Kind: knowledge.Person, Name: "José", Fact: text})
			if err := s.ApplyEnrichment(ctx, chunk); err != nil {
				t.Fatal(i, source, err)
			}
		}
	}
	legacyCount := 0
	// The legacy algorithm also had the posting scan reserved on top of the default.
	if err := s.viewWithReadLimit(ctx, bolt.MaxTransactionOps+enrich.MaxNameEntries, func(reader bolt.Reader) error {
		ids, err := s.inferredNameIDs(reader, "José")
		if err != nil {
			return err
		}
		for _, id := range ids {
			record, found, err := s.readInferredRecord(reader, id)
			if err != nil {
				return err
			}
			if !found {
				return ErrCorrupt
			}
			if err := s.loadInferredFactWindow(reader, &record); err != nil {
				return err
			}
			if err := s.currentInferredRecord(reader, &record); err != nil {
				return err
			}
			if len(record.Entity.Facts) > 0 {
				legacyCount++
			}
		}
		return nil
	}); err != nil {
		t.Fatal("legacy algorithm unexpectedly failed", err)
	}
	if legacyCount != enrich.MaxNameEntries {
		t.Fatal("legacy count", legacyCount)
	}
	t.Logf("Legacy exact-name algorithm returned all %d contexts within default read budget", legacyCount)
	rows, err := s.FindInferredKnowledge(ctx, "José")
	if err != nil || len(rows) != enrich.MaxNameEntries {
		t.Fatalf("folded lookup regressed a valid exact-name read: rows=%d err=%v", len(rows), err)
	}
}
