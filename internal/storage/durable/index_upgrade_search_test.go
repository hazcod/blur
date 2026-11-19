package durable

import (
	"context"
	"testing"
	"time"
)

// Search after upgrading a store that holds both a plain observation and a
// published multipart capture, both marked at the previous format. The
// observation index is rebuilt; the capture-part index keeps its marker and
// pages. Chosen fix: decodeSearchIndexState accepts the structure-version
// marker for the capture-part scope, so open writes nothing to old stores and
// every existing capture-part page stays decodable (rewriting the marker at
// open would also work but adds a write to every upgrade for no benefit).
func TestSearchAfterFormatUpgradeFindsObservationsAndPublishedCaptures(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0x66)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	plain := indexObservation(t, "terminal.app", "", "retained observation mentioning zebrafish")
	if err := store.PutObservation(ctx, plain); err != nil {
		t.Fatal(err)
	}
	publishIndexedFixture(t, store, "Mira owns the rollback plan for quokka release.", time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC))
	drainCaptureIndex(t, store)
	if _, err := store.IndexPendingExact(ctx, 32); err != nil {
		t.Fatal(err)
	}

	// Both ready markers as the previous release wrote them (format 4).
	for _, scope := range []*Store{store, store.capturePartIndex()} {
		marker, err := scope.seal(scope.exactStateKey(), scope.exactNamespace("search-index-state-v1"), []byte{searchIndexFormatVersion - 1, 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Update(ctx, func(tx *Tx) error {
			tx.mu.Lock()
			defer tx.mu.Unlock()
			return (&searchIndexTx{Tx: tx, store: scope}).writeEntryLocked(scope.exactStateKey(), marker, true)
		}); err != nil {
			t.Fatal(err)
		}
		clear(marker)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatalf("open store at the previous format: %v", err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	for rounds := 0; ; rounds++ {
		more, err := store.IndexPendingExact(ctx, 32)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
		if rounds > 1000 {
			t.Fatal("observation rebuild did not finish")
		}
	}
	drainCaptureIndex(t, store)

	// Capture-part scope alone.
	if page, err := store.capturePartIndex().SearchExact(ctx, scopedQuery("quokka")); err != nil || len(page.Hits) != 1 {
		t.Fatalf("capture-part search after upgrade: %+v err=%v", page, err)
	}
	// Combined path: observation index plus capture parts.
	if page, err := store.SearchExact(ctx, scopedQuery("quokka")); err != nil || len(page.Hits) != 1 || page.IndexingPending {
		t.Fatalf("combined search for a capture term after upgrade: %+v err=%v", page, err)
	}
	if page, err := store.SearchExact(ctx, scopedQuery("zebrafish")); err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != plain.ContentID() {
		t.Fatalf("combined search for an observation term after upgrade: %+v err=%v", page, err)
	}
	// An unrelated term still comes back empty rather than corrupt.
	if page, err := store.SearchExact(ctx, scopedQuery("absentterm")); err != nil || len(page.Hits) != 0 {
		t.Fatalf("absent term: %+v err=%v", page, err)
	}
	// A second open needs no rebuild and still searches.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(again.Close)
	if page, err := again.SearchExact(ctx, scopedQuery("quokka")); err != nil || len(page.Hits) != 1 {
		t.Fatalf("search after second open: %+v err=%v", page, err)
	}
}
