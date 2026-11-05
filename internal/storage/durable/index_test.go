package durable

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/nielsgroen/blur/internal/capture"
	"github.com/nielsgroen/blur/internal/search"
	"github.com/nielsgroen/blur/internal/storage"
)

func indexObservation(t *testing.T, app, domain, text string) capture.SanitizedObservation {
	t.Helper()
	observation, err := capture.SanitizeObservation(app, domain, text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

func TestDurableExactIndexSearchesWithoutScanFallbackAndAppliesFilters(t *testing.T) {
	store, err := OpenDurable(t.TempDir()+"/search.db", durableTestKey(t, 0x71), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	matches := []capture.SanitizedObservation{
		indexObservation(t, "editor.app", "docs.example", "alpha beta notes"),
		indexObservation(t, "editor.app", "docs.example", "alpha only"),
		indexObservation(t, "browser.app", "docs.example", "alpha beta notes"),
		indexObservation(t, "editor.app", "mail.example", "alpha beta notes"),
	}
	for _, observation := range matches {
		if err := store.PutObservation(context.Background(), observation); err != nil {
			t.Fatal(err)
		}
	}
	request := search.Request{
		Query: "BETA alpha", AppID: " EDITOR.APP ", Domain: "DOCS.EXAMPLE.",
		ResultLimit: 10, SnippetBytes: 64, ResponseBytes: 4096, ScanLimit: 1,
	}
	response, err := search.New(store).Search(context.Background(), request)
	if err != nil {
		t.Fatalf("indexed search failed without scan fallback: %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].SourceID != matches[0].ContentID() {
		t.Fatalf("indexed AND/filter result = %+v, want only source %s", response.Results, matches[0].ContentID())
	}
	if response.Results[0].Score != 2 || response.Results[0].ProvenanceKind != "raw_sanitized_observation" {
		t.Fatalf("rank or source provenance = %+v", response.Results[0])
	}
}

func TestDurableSearchUsesDeterministicRankAndSourceIDTieBreak(t *testing.T) {
	observations := []capture.SanitizedObservation{
		indexObservation(t, "editor.app", "docs.example", "alpha beta first"),
		indexObservation(t, "editor.app", "docs.example", "alpha beta second"),
		indexObservation(t, "editor.app", "docs.example", "alpha beta third"),
	}
	request := search.Request{Query: "alpha beta", ResultLimit: 3, SnippetBytes: 64, ResponseBytes: 4096, ScanLimit: 1}
	var want []string
	for run := 0; run < 2; run++ {
		store, err := OpenDurable(t.TempDir()+"/search.db", durableTestKey(t, byte(0x72+run)), CreateNew)
		if err != nil {
			t.Fatal(err)
		}
		for i := range observations {
			index := i
			if run == 1 {
				index = len(observations) - 1 - i
			}
			if err := store.PutObservation(context.Background(), observations[index]); err != nil {
				t.Fatal(err)
			}
		}
		response, err := search.New(store).Search(context.Background(), request)
		if err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		got := []string{response.Results[0].SourceID, response.Results[1].SourceID, response.Results[2].SourceID}
		if run == 0 {
			want = got
		} else if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Fatalf("insertion order changed tie ranking: %v vs %v", want, got)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDurableIndexBoundsRowsAndEncryptedBytes(t *testing.T) {
	store, err := OpenDurable(t.TempDir()+"/search.db", durableTestKey(t, 0x79), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := 0; i < 4; i++ {
		if err := store.PutObservation(context.Background(), indexObservation(t, "editor.app", "docs.example", fmt.Sprintf("alpha item%02d", i))); err != nil {
			t.Fatal(err)
		}
	}
	request := search.Request{Query: "alpha", ResultLimit: 10, SnippetBytes: 32, ResponseBytes: 4096, ScanLimit: 1, IndexLimit: 1}
	response, err := search.New(store).Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Scanned != 1 || !response.Truncated {
		t.Fatalf("row bound not honored: %+v", response)
	}
	request.IndexLimit = 0
	request.IndexBytes = 1
	response, err = search.New(store).Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.ScannedBytes > 1 || len(response.Results) != 0 || !response.Truncated {
		t.Fatalf("encrypted index byte bound not honored: %+v", response)
	}
}

func TestDurableExactIndexHonorsCancellation(t *testing.T) {
	store, err := OpenDurable(t.TempDir()+"/search.db", durableTestKey(t, 0x7a), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.PutObservation(context.Background(), indexObservation(t, "editor.app", "docs.example", "alpha")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.SearchExact(ctx, search.ExactQuery{Terms: []string{"alpha"}, MaxRows: 1, MaxBytes: search.MaxIndexBytes})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchExact(canceled) error = %v, want context.Canceled", err)
	}
}

func TestDurableSearchIndexNeverPersistsPlaintextTermsOrLogicalIDs(t *testing.T) {
	path := t.TempDir() + "/search.db"
	store, err := OpenDurable(path, durableTestKey(t, 0x74), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	observation := indexObservation(t, "private-editor-sentinel", "private-domain-sentinel", "private-term-sentinel alpha")
	if err := store.PutObservation(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{"private-editor-sentinel", "private-domain-sentinel", "private-term-sentinel", observation.ContentID()} {
		if strings.Contains(string(encoded), sentinel) {
			t.Fatalf("physical database contains plaintext search material %q", sentinel)
		}
	}
}

func TestDurableObservationAndIndexMutationRollBackTogether(t *testing.T) {
	store, err := OpenDurable(t.TempDir()+"/search.db", durableTestKey(t, 0x75), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	observation := indexObservation(t, "editor.app", "docs.example", "rollback-marker alpha")
	err = store.Update(context.Background(), func(tx *Tx) error {
		if err := tx.PutObservation(observation); err != nil {
			return err
		}
		return errors.New("synthetic rollback")
	})
	if !errors.Is(err, ErrTransaction) {
		t.Fatalf("Update(rollback) error = %v, want ErrTransaction", err)
	}
	if _, found, err := store.GetObservation(context.Background(), observation.ContentID()); err != nil || found {
		t.Fatalf("observation survived rollback: found=%t err=%v", found, err)
	}
	response, err := search.New(store).Search(context.Background(), search.Request{Query: "rollback-marker", ResultLimit: 2, SnippetBytes: 64, ResponseBytes: 4096, ScanLimit: 1})
	if err != nil {
		t.Fatalf("search after rollback failed: %v", err)
	}
	if len(response.Results) != 0 {
		t.Fatalf("posting survived observation rollback: %+v", response.Results)
	}
}

func TestDurableIndexDeleteRemovesPostingsAndRollsBackAtomically(t *testing.T) {
	store, err := OpenDurable(t.TempDir()+"/search.db", durableTestKey(t, 0x76), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := indexObservation(t, "editor.app", "docs.example", "alpha beta first")
	second := indexObservation(t, "editor.app", "docs.example", "alpha beta second")
	for _, observation := range []capture.SanitizedObservation{first, second} {
		if err := store.PutObservation(context.Background(), observation); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Update(context.Background(), func(tx *Tx) error {
		if err := tx.deleteObservation(first.ContentID()); err != nil {
			return err
		}
		return errors.New("synthetic rollback")
	}); !errors.Is(err, ErrTransaction) {
		t.Fatalf("delete rollback error = %v, want ErrTransaction", err)
	}
	if _, found, err := store.GetObservation(context.Background(), first.ContentID()); err != nil || !found {
		t.Fatalf("rolled-back observation missing: found=%t err=%v", found, err)
	}
	if err := store.Update(context.Background(), func(tx *Tx) error { return tx.deleteObservation(first.ContentID()) }); err != nil {
		t.Fatal(err)
	}
	response, err := search.New(store).Search(context.Background(), search.Request{Query: "alpha beta", ResultLimit: 10, SnippetBytes: 64, ResponseBytes: 4096, ScanLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].SourceID != second.ContentID() {
		t.Fatalf("delete left a stale posting or removed a sibling: %+v", response.Results)
	}
}

func TestDurableIndexPagesAndRebuildsBeforeReopenReturns(t *testing.T) {
	path := t.TempDir() + "/search.db"
	store, err := OpenDurable(path, durableTestKey(t, 0x77), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	observations := make([]capture.SanitizedObservation, 70)
	for i := range observations {
		observations[i] = indexObservation(t, "editor.app", "docs.example", fmt.Sprintf("alpha beta item%02d", i))
		if err := store.PutObservation(context.Background(), observations[i]); err != nil {
			t.Fatal(err)
		}
	}
	indices := []int{0}
	for i := searchIndexPageRows; i < len(observations); i++ {
		indices = append(indices, i)
	}
	for _, index := range indices {
		if err := store.Update(context.Background(), func(tx *Tx) error { return tx.deleteObservation(observations[index].ContentID()) }); err != nil {
			t.Fatal(err)
		}
	}
	newObservation := indexObservation(t, "editor.app", "docs.example", "alpha beta after-delete")
	if err := store.PutObservation(context.Background(), newObservation); err != nil {
		t.Fatalf("append to empty trailing index page: %v", err)
	}
	if err := store.clearSearchIndex(context.Background()); err != nil {
		t.Fatalf("remove index for legacy fixture: %v", err)
	}
	if err := store.Update(context.Background(), func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return tx.indexObservationLocked(observations[1])
	}); err != nil {
		t.Fatalf("write partial rebuild fixture: %v", err)
	}
	if _, err := store.SearchExact(context.Background(), search.ExactQuery{Terms: []string{"alpha"}, MaxRows: 10, MaxBytes: search.MaxIndexBytes}); !errors.Is(err, search.ErrIndexUnavailable) {
		t.Fatalf("partial index without ready marker was exposed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, durableTestKey(t, 0x77), OpenExisting)
	if err != nil {
		t.Fatalf("rebuild old observation set: %v", err)
	}
	defer store.Close()
	response, err := search.New(store).Search(context.Background(), search.Request{Query: "alpha beta", ResultLimit: 100, SnippetBytes: 32, ResponseBytes: 64 << 10, ScanLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 64 || response.Truncated {
		t.Fatalf("rebuilt multi-page index result count=%d truncated=%t, want 64 exact results", len(response.Results), response.Truncated)
	}
}

func TestDurableIndexRejectsObservationWithUnboundedTermSet(t *testing.T) {
	store, err := OpenDurable(t.TempDir()+"/search.db", durableTestKey(t, 0x78), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var text strings.Builder
	for i := 0; i <= search.MaxIndexedObservationTerms; i++ {
		fmt.Fprintf(&text, "term%03d ", i)
	}
	observation := indexObservation(t, "editor.app", "docs.example", text.String())
	err = store.PutObservation(context.Background(), observation)
	var storeError *storage.StoreError
	if !errors.As(err, &storeError) || storeError.Code != storage.Invalid {
		t.Fatalf("over-limit observation error = %v, want storage.Invalid", err)
	}
	if _, found, err := store.GetObservation(context.Background(), observation.ContentID()); err != nil || found {
		t.Fatalf("over-limit observation partially stored: found=%t err=%v", found, err)
	}
}

func FuzzDecodeSearchIndexPage(f *testing.F) {
	f.Add([]byte{searchIndexFormatVersion, 0})
	f.Add([]byte{searchIndexFormatVersion, 1, 0})
	f.Fuzz(func(t *testing.T, encoded []byte) {
		postings, err := decodeSearchIndexPage(encoded)
		if err != nil {
			return
		}
		if _, err := encodeSearchIndexPage(postings); err != nil {
			t.Fatalf("decoded page could not be canonically encoded: %v", err)
		}
	})
}

func FuzzDecodeSearchIndexReverse(f *testing.F) {
	f.Add([]byte{searchIndexFormatVersion, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, encoded []byte) {
		sourceBytes, refs, err := decodeSearchIndexReverse(encoded)
		if err != nil {
			return
		}
		if _, err := encodeSearchIndexReverse(sourceBytes, refs); err != nil {
			t.Fatalf("decoded reverse record could not be encoded: %v", err)
		}
	})
}
