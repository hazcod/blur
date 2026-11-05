package durable

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/nielsgroen/blur/internal/capture"
	"github.com/nielsgroen/blur/internal/search"
	"github.com/nielsgroen/blur/internal/storage"
	"github.com/nielsgroen/blur/internal/storage/physical/bolt"
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

func TestDurableSearchReturnsGlobalTopKAcrossIngestionOrders(t *testing.T) {
	observations := make([]capture.SanitizedObservation, 0, 1006)
	for i := 0; i < 1003; i++ {
		observations = append(observations, indexObservation(t, "editor.app", "docs.example", fmt.Sprintf("alpha ordinary-%04d", i)))
	}
	highScore := indexObservation(t, "alpha.app", "docs.example", "highest rank")
	tieA := indexObservation(t, "editor.app", "alpha.example", "tie a")
	tieB := indexObservation(t, "editor.app", "alpha.example", "tie b")
	observations = append(observations, highScore, tieA, tieB)
	if tieA.ContentID() < tieB.ContentID() {
		tieA, tieB = tieB, tieA
	}
	want := []string{highScore.ContentID(), tieB.ContentID(), tieA.ContentID()}
	request := search.Request{Query: "alpha", ResultLimit: 3, SnippetBytes: 64, ResponseBytes: 4096, ScanLimit: 1}

	for _, reverse := range []bool{false, true} {
		path := t.TempDir() + "/search.db"
		store, err := OpenDurable(path, durableTestKey(t, 0x8a), CreateNew)
		if err != nil {
			t.Fatal(err)
		}
		for start := 0; start < len(observations); {
			batchSize := 4
			if start < 75 {
				batchSize = 1
			}
			end := min(start+batchSize, len(observations))
			batch := observations[start:end]
			if reverse {
				batch = make([]capture.SanitizedObservation, len(observations[start:end]))
				for i := range batch {
					batch[i] = observations[len(observations)-1-(start+i)]
				}
			}
			if err := store.Update(context.Background(), func(tx *Tx) error {
				for _, observation := range batch {
					if err := tx.PutObservation(observation); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				_ = store.Close()
				t.Fatalf("reverse=%t batch start=%d: %v", reverse, start, err)
			}
			start = end
		}
		response, err := search.New(store).Search(context.Background(), request)
		if err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		if response.Scanned > search.MaxIndexRows || !response.Truncated || len(response.Results) != len(want) {
			t.Fatalf("reverse=%t bounded top-k response = %+v", reverse, response)
		}
		got := make([]string, len(response.Results))
		for i := range response.Results {
			got[i] = response.Results[i].SourceID
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("reverse=%t global top-k = %v, want score-descending and SourceID tie order %v", reverse, got, want)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		store, err = OpenDurable(path, durableTestKey(t, 0x8a), OpenExisting)
		if err != nil {
			t.Fatal(err)
		}
		filtered, err := search.New(store).Search(context.Background(), search.Request{
			Query: "alpha", Domain: "alpha.example", ResultLimit: 3, SnippetBytes: 64,
			ResponseBytes: 4096, ScanLimit: 1,
		})
		if err != nil || len(filtered.Results) != 2 || filtered.Results[0].SourceID != tieB.ContentID() || filtered.Results[1].SourceID != tieA.ContentID() {
			_ = store.Close()
			t.Fatalf("reverse=%t reopened domain-filtered tie results = %+v, err=%v", reverse, filtered, err)
		}
		if err := store.Update(context.Background(), func(tx *Tx) error { return tx.deleteObservation(tieB.ContentID()) }); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		filtered, err = search.New(store).Search(context.Background(), search.Request{
			Query: "alpha", Domain: "alpha.example", ResultLimit: 3, SnippetBytes: 64,
			ResponseBytes: 4096, ScanLimit: 1,
		})
		if err != nil || len(filtered.Results) != 1 || filtered.Results[0].SourceID != tieA.ContentID() {
			_ = store.Close()
			t.Fatalf("reverse=%t delete left a stale or removed sibling posting: %+v, err=%v", reverse, filtered.Results, err)
		}
		if err := store.PutObservation(context.Background(), tieB); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		filtered, err = search.New(store).Search(context.Background(), search.Request{
			Query: "alpha", AppID: "alpha.app", ResultLimit: 3, SnippetBytes: 64,
			ResponseBytes: 4096, ScanLimit: 1,
		})
		if err != nil || len(filtered.Results) != 1 || filtered.Results[0].SourceID != highScore.ContentID() {
			_ = store.Close()
			t.Fatalf("reverse=%t app-filtered result = %+v, err=%v", reverse, filtered.Results, err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

type operationCountingWriter struct {
	bolt.Writer
	operations int
}

func (w *operationCountingWriter) Get(key []byte) ([]byte, bool, error) {
	w.operations++
	return w.Writer.Get(key)
}

func (w *operationCountingWriter) Has(key []byte) (bool, error) {
	w.operations++
	return w.Writer.Has(key)
}

func (w *operationCountingWriter) Scan(prefix, after []byte, limit, maxBytes int) ([]bolt.Entry, bool, error) {
	w.operations++
	return w.Writer.Scan(prefix, after, limit, maxBytes)
}

func (w *operationCountingWriter) Put(key, value []byte) error {
	w.operations++
	return w.Writer.Put(key, value)
}

func (w *operationCountingWriter) Delete(key []byte) error {
	w.operations++
	return w.Writer.Delete(key)
}

func TestDurableIndexCommonTermLeafSplitStaysBelowTransactionOperationCap(t *testing.T) {
	store, err := OpenDurable(t.TempDir()+"/search.db", durableTestKey(t, 0x8b), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const commonTermCount = search.MaxIndexedObservationTerms - 2 // app and domain tokens fill the remaining two terms
	common := make([]string, commonTermCount)
	for i := range common {
		common[i] = fmt.Sprintf("common%03d", i)
	}
	for i := 0; i < searchIndexPageRows; i++ {
		text := strings.Join(common, " ") + strings.Repeat("!", i+1)
		if err := store.PutObservation(context.Background(), indexObservation(t, "editor", "docs", text)); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	// 126 common text terms plus one app and one domain token reaches the
	// 128-term ceiling. Punctuation gives each observation a unique content ID
	// without adding any more normalized terms. The 65th posting splits all 128
	// posting leaves in the same atomic observation mutation.
	text := strings.Join(common, " ") + strings.Repeat("!", searchIndexPageRows+1)
	observation := indexObservation(t, "editor", "docs", text)
	var counting *operationCountingWriter
	err = store.Update(context.Background(), func(tx *Tx) error {
		counting = &operationCountingWriter{Writer: tx.writer}
		tx.writer = counting
		return tx.PutObservation(observation)
	})
	if err != nil {
		t.Fatalf("128-term common split mutation failed: %v", err)
	}
	// Update loads the manifest once before the writer wrapper is installed.
	operations := counting.operations + 1
	t.Logf("128-term 64-to-65 common-posting split used %d physical transaction operations", operations)
	if operations >= 900 || operations >= bolt.MaxTransactionOps {
		t.Fatalf("128-term simultaneous leaf split used %d physical ops; want <900 for transaction headroom (hard cap %d)", operations, bolt.MaxTransactionOps)
	}
}

func TestDurableIndexPackedBranchSplitAndDeleteRemainOrdered(t *testing.T) {
	store, err := OpenDurable(t.TempDir()+"/search.db", durableTestKey(t, 0x8c), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const leafCount = searchIndexTreeMaxChildren
	const rowsPerLeaf = searchIndexPageRows
	kind := searchIndexTerm
	digest := store.searchLookupDigest(kind, "branch-split-fixture")
	defer clear(digest[:])
	var root [sha256.Size]byte
	children := make([]searchIndexTreeChild, leafCount)
	firstOrders := make([][searchIndexTreeOrderBytes]byte, leafCount)
	for leaf := 0; leaf < leafCount; leaf++ {
		firstID := fmt.Sprintf("%064x", leaf*rowsPerLeaf)
		lastID := fmt.Sprintf("%064x", (leaf+1)*rowsPerLeaf-1)
		first, err := searchIndexOrderKey(kind, 1, firstID)
		if err != nil {
			t.Fatal(err)
		}
		last, err := searchIndexOrderKey(kind, 1, lastID)
		if err != nil {
			clear(first)
			t.Fatal(err)
		}
		copy(firstOrders[leaf][:], first)
		copy(children[leaf].upper[:], last)
		children[leaf].id = store.searchIndexTreeChildID(kind, digest, root, firstOrders[leaf], searchIndexTreeLeaf, 1)
		clear(first)
		clear(last)
	}

	const seedBatch = 128
	for start := 0; start < leafCount; start += seedBatch {
		end := min(start+seedBatch, leafCount)
		currentChildren := append([]searchIndexTreeChild(nil), children[:end]...)
		currentChildren[len(currentChildren)-1].upper = searchIndexTreeInfinity
		if err := store.Update(context.Background(), func(tx *Tx) error {
			for leaf := start; leaf < end; leaf++ {
				postings := make([]searchIndexPosting, rowsPerLeaf)
				for row := range postings {
					id := fmt.Sprintf("%064x", leaf*rowsPerLeaf+row)
					postings[row] = searchIndexPosting{sourceID: id, fields: 1}
				}
				if err := tx.putSearchIndexTreeNodeLocked(kind, digest, children[leaf].id, searchIndexTreeNode{leaf: true, postings: postings}, nil); err != nil {
					return err
				}
			}
			_, oldRoot, found, err := tx.readSearchIndexTreeNodeLocked(kind, digest, root)
			if err != nil {
				return err
			}
			if start == 0 && found || start > 0 && !found {
				clear(oldRoot)
				return ErrCorrupt
			}
			defer clear(oldRoot)
			return tx.putSearchIndexTreeNodeLocked(kind, digest, root, searchIndexTreeNode{children: currentChildren}, oldRoot)
		}); err != nil {
			t.Fatalf("seed packed tree leaf range [%d,%d): %v", start, end, err)
		}
	}

	newID := fmt.Sprintf("%064x", leafCount*rowsPerLeaf)
	ordered, err := searchIndexOrderKey(kind, 1, newID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(tx *Tx) error {
		return tx.insertSearchIndexTreeLocked(kind, digest, ordered, searchIndexPosting{sourceID: newID, fields: 1})
	}); err != nil {
		clear(ordered)
		t.Fatalf("root packed-branch split: %v", err)
	}
	clear(ordered)

	checkCount := func(wantCount int) {
		t.Helper()
		iterator := newSearchIndexTreeIterator(context.Background(), nil, store, kind, digest)
		page := search.ExactPage{}
		count, totalNodes := 0, 0
		done := false
		for !done {
			err := store.view(context.Background(), func(reader bolt.Reader) error {
				iterator.reader = reader
				page.Nodes = 0 // each read snapshot stays below the physical op cap
				for page.Nodes < 500 {
					posting, ok, stopped, err := iterator.next(&page, search.MaxIndexBytes)
					if err != nil || stopped {
						return fmt.Errorf("iterate packed tree at %d: posting=%t stopped=%t err=%v", count, ok, stopped, err)
					}
					if !ok {
						done = true
						break
					}
					wantID := fmt.Sprintf("%064x", count)
					if posting.sourceID != wantID || posting.fields != 1 {
						return fmt.Errorf("packed tree order at %d = %s/%d, want %s/1", count, posting.sourceID, posting.fields, wantID)
					}
					count++
				}
				if page.Nodes > search.MaxIndexNodes || page.Bytes > search.MaxIndexBytes {
					return fmt.Errorf("packed tree traversal exceeded bounds: %+v", page)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			totalNodes += page.Nodes
		}
		if count != wantCount {
			t.Fatalf("packed tree yielded %d postings, want %d", count, wantCount)
		}
		if totalNodes > search.MaxIndexNodes+8 {
			t.Fatalf("packed tree node reads = %d, want small fixed-depth overhead beyond %d leaf pages", totalNodes, leafCount)
		}
	}
	checkCount(leafCount*rowsPerLeaf + 1)

	if err := store.Update(context.Background(), func(tx *Tx) error {
		for id := (leafCount-1)*rowsPerLeaf + rowsPerLeaf/2; id <= leafCount*rowsPerLeaf; id++ {
			if err := tx.removeSearchIndexTreePostingLocked(kind, digest, fmt.Sprintf("%064x", id), 1); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("remove split-right leaf: %v", err)
	}
	checkCount(leafCount*rowsPerLeaf - rowsPerLeaf/2)
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
	if !errors.Is(err, search.ErrIndexBudget) || len(response.Results) != 0 {
		t.Fatalf("unproven row-limited ranking = %+v, err=%v; want fail-closed budget error", response, err)
	}
	request.IndexLimit = 0
	request.IndexBytes = 1
	response, err = search.New(store).Search(context.Background(), request)
	if !errors.Is(err, search.ErrIndexBudget) || len(response.Results) != 0 {
		t.Fatalf("unproven byte-limited ranking = %+v, err=%v; want fail-closed budget error", response, err)
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
		t.Fatalf("index observation after deletion: %v", err)
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

func TestDurableIndexRebuildsLegacyFormatBeforeReturning(t *testing.T) {
	path := t.TempDir() + "/legacy-search.db"
	key := durableTestKey(t, 0x8b)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	want := indexObservation(t, "editor.app", "docs.example", "legacy alpha beta")
	if err := store.PutObservation(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	legacyMarker, err := store.seal(searchIndexStateKey, "search-index-state-v1", []byte{1, 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return tx.writeEntryLocked(searchIndexStateKey, legacyMarker, true)
	}); err != nil {
		clear(legacyMarker)
		_ = store.Close()
		t.Fatalf("write legacy ready marker: %v", err)
	}
	clear(legacyMarker)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatalf("rebuild index from legacy format: %v", err)
	}
	defer store.Close()
	response, err := search.New(store).Search(context.Background(), search.Request{
		Query: "alpha beta", ResultLimit: 2, SnippetBytes: 64,
		ResponseBytes: 4096, ScanLimit: 1,
	})
	if err != nil || len(response.Results) != 1 || response.Results[0].SourceID != want.ContentID() {
		t.Fatalf("legacy rebuild result = %+v, err=%v", response.Results, err)
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

func FuzzDecodeSearchIndexTreeNode(f *testing.F) {
	leaf, err := encodeSearchIndexTreeNode(searchIndexTreeNode{
		leaf:     true,
		postings: []searchIndexPosting{{sourceID: strings.Repeat("0", 64), fields: 1}},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(leaf, searchIndexTerm)
	var childID [sha256.Size]byte
	childID[0] = 1
	branch, err := encodeSearchIndexTreeNode(searchIndexTreeNode{children: []searchIndexTreeChild{{upper: searchIndexTreeInfinity, id: childID}}})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(branch, searchIndexApp)
	f.Fuzz(func(t *testing.T, encoded []byte, kind byte) {
		node, err := decodeSearchIndexTreeNode(encoded, kind)
		if err != nil {
			return
		}
		canonical, err := encodeSearchIndexTreeNode(node)
		if err != nil {
			t.Fatalf("decoded tree node could not be canonically encoded: %v", err)
		}
		if !bytes.Equal(canonical, encoded) {
			t.Fatalf("tree node canonical round trip changed bytes: %x != %x", canonical, encoded)
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
