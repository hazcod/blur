package durable

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
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
	defer func() { _ = store.Close() }()

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

func TestDurableSearchSupportsBoundedMinimumTermMatches(t *testing.T) {
	store, err := OpenDurable(t.TempDir()+"/search.db", durableTestKey(t, 0x7c), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	all := indexObservation(t, "editor.app", "docs.example", "anna q3 budget approved")
	budgetAnna := indexObservation(t, "editor.app", "docs.example", "anna budget discussion")
	q3Anna := indexObservation(t, "editor.app", "docs.example", "anna q3 discussion")
	one := indexObservation(t, "editor.app", "docs.example", "budget discussion")
	for _, observation := range []capture.SanitizedObservation{all, budgetAnna, q3Anna, one} {
		if err := store.PutObservation(context.Background(), observation); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.SearchExact(context.Background(), search.ExactQuery{
		Terms: []string{"anna", "budget", "q3"}, MinTermMatches: 2,
		TopK: 4, MaxRows: 32, MaxBytes: search.MaxIndexBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 3 || page.Hits[0].SourceID != all.ContentID() || page.HasMore {
		t.Fatalf("minimum-match page=%+v, want the 3-term result ranked first and all 2-term results, excluding one-term result %s", page, one.ContentID())
	}
	for _, hit := range page.Hits {
		if hit.SourceID == one.ContentID() {
			t.Fatalf("minimum-match query included one-term candidate %s", hit.SourceID)
		}
	}
	if _, err := store.SearchExact(context.Background(), search.ExactQuery{
		Terms: []string{"anna", "budget"}, MinTermMatches: 3,
		TopK: 1, MaxRows: 8, MaxBytes: search.MaxIndexBytes,
	}); !errors.Is(err, search.ErrInvalidRequest) {
		t.Fatalf("invalid minimum-term threshold error=%v, want ErrInvalidRequest", err)
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

func TestDurableDeferredSiblingSearchSurvivesReopenAndDelete(t *testing.T) {
	path := t.TempDir() + "/search.db"
	key := durableTestKey(t, 0x9a)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	first := indexObservation(t, "editor.app", "docs.example", "alpha left")
	second := indexObservation(t, "editor.app", "docs.example", "alpha right")
	for _, observation := range []capture.SanitizedObservation{first, second} {
		if err := store.PutObservation(context.Background(), observation); err != nil {
			t.Fatal(err)
		}
	}
	digest := store.searchLookupDigest(searchIndexTerm, "alpha")
	defer clear(digest[:])
	if err := installDeferredSiblingForTwoPostings(store, digest); err != nil {
		t.Fatalf("install deferred sibling fixture: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatalf("reopen with an unrepaired sibling link: %v", err)
	}
	defer store.Close()
	query := search.ExactQuery{Terms: []string{"alpha"}, TopK: 3, MaxRows: 16, MaxBytes: search.MaxIndexBytes}
	page, err := store.SearchExact(context.Background(), query)
	if err != nil {
		t.Fatalf("search across persisted sibling link: %v", err)
	}
	want := []string{first.ContentID(), second.ContentID()}
	sort.Strings(want)
	if len(page.Hits) != 2 || page.Hits[0].SourceID != want[0] || page.Hits[1].SourceID != want[1] || page.HasMore {
		t.Fatalf("reopened sibling query page=%+v, want both exact postings %v with no more", page, want)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.SearchExact(canceled, query); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled sibling query error=%v, want context.Canceled", err)
	}
	if err := store.Update(context.Background(), func(tx *Tx) error {
		if err := tx.deleteObservation(want[1]); err != nil {
			return err
		}
		return errors.New("rollback deferred-link delete")
	}); !errors.Is(err, ErrTransaction) {
		t.Fatalf("deferred-link delete rollback error=%v, want ErrTransaction", err)
	}
	page, err = store.SearchExact(context.Background(), query)
	if err != nil || len(page.Hits) != 2 {
		t.Fatalf("rolled-back sibling delete page=%+v err=%v, want both postings", page, err)
	}
	if err := store.Update(context.Background(), func(tx *Tx) error { return tx.deleteObservation(want[1]) }); err != nil {
		t.Fatalf("delete through deferred sibling: %v", err)
	}
	page, err = store.SearchExact(context.Background(), query)
	if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != want[0] || page.HasMore {
		t.Fatalf("post-delete sibling query page=%+v err=%v, want only %s", page, err, want[0])
	}
}

func installDeferredSiblingForTwoPostings(store *Store, digest [sha256.Size]byte) error {
	var root [sha256.Size]byte
	return store.Update(context.Background(), func(tx *Tx) error {
		rootNode, rootEnvelope, found, err := tx.readSearchIndexTreeNodeLocked(searchIndexTerm, digest, root)
		if err != nil || !found || !rootNode.leaf || len(rootNode.postings) != 2 {
			clear(rootEnvelope)
			if err != nil {
				return err
			}
			return fmt.Errorf("alpha root leaf found=%t leaf=%t rows=%d", found, rootNode.leaf, len(rootNode.postings))
		}
		defer clear(rootEnvelope)
		postings := append([]searchIndexPosting(nil), rootNode.postings...)
		sortSearchIndexPostings(postings)
		pivotBytes, err := searchIndexOrderKey(searchIndexTerm, postings[0].fields, postings[0].sourceID)
		if err != nil {
			return err
		}
		var pivot [searchIndexTreeOrderBytes]byte
		copy(pivot[:], pivotBytes)
		clear(pivotBytes)
		leftID, err := newSearchIndexTreeNodeID()
		if err != nil {
			return err
		}
		rightID, err := newSearchIndexTreeNodeID()
		if err != nil {
			return err
		}
		branchID, err := newSearchIndexTreeNodeID()
		if err != nil {
			return err
		}
		if err := tx.putSearchIndexTreeNodeLocked(searchIndexTerm, digest, leftID, searchIndexTreeNode{leaf: true, postings: postings[:1]}, nil); err != nil {
			return err
		}
		if err := tx.putSearchIndexTreeNodeLocked(searchIndexTerm, digest, rightID, searchIndexTreeNode{leaf: true, postings: postings[1:]}, nil); err != nil {
			return err
		}
		linked := searchIndexTreeNode{
			children:     []searchIndexTreeChild{{upper: pivot, id: leftID}},
			rightSibling: rightID,
			siblingPivot: pivot,
		}
		if err := tx.putSearchIndexTreeNodeLocked(searchIndexTerm, digest, branchID, linked, nil); err != nil {
			return err
		}
		return tx.putSearchIndexTreeNodeLocked(searchIndexTerm, digest, root, searchIndexTreeNode{
			children: []searchIndexTreeChild{{upper: searchIndexTreeInfinity, id: branchID}},
		}, rootEnvelope)
	})
}

type operationCountingWriter struct {
	bolt.Writer
	operations int
	bytes      int
}

func (w *operationCountingWriter) Get(key []byte) ([]byte, bool, error) {
	w.operations++
	value, found, err := w.Writer.Get(key)
	w.bytes += len(key) + len(value)
	return value, found, err
}

func (w *operationCountingWriter) Has(key []byte) (bool, error) {
	w.operations++
	w.bytes += len(key)
	return w.Writer.Has(key)
}

func (w *operationCountingWriter) Scan(prefix, after []byte, limit, maxBytes int) ([]bolt.Entry, bool, error) {
	w.operations++
	entries, more, err := w.Writer.Scan(prefix, after, limit, maxBytes)
	for _, entry := range entries {
		w.bytes += len(entry.Key) + len(entry.Value)
	}
	return entries, more, err
}

func (w *operationCountingWriter) Put(key, value []byte) error {
	w.operations++
	w.bytes += len(key) + len(value)
	return w.Writer.Put(key, value)
}

func (w *operationCountingWriter) Delete(key []byte) error {
	w.operations++
	w.bytes += len(key)
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

func TestDurableIndexMaxTermSynchronizedNonRootSplitStaysBounded(t *testing.T) {
	path := t.TempDir() + "/search.db"
	key := durableTestKey(t, 0x91)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	common := make([]string, 126)
	for i := range common {
		common[i] = fmt.Sprintf("shared%03d", i)
	}
	base := make([]capture.SanitizedObservation, searchIndexPageRows)
	baseIDs := make([]string, len(base))
	for i := range base {
		base[i] = indexObservation(t, "editor", "docs", strings.Join(common, " ")+strings.Repeat("!", i+1))
		baseIDs[i] = base[i].ContentID()
		if err := store.PutObservation(context.Background(), base[i]); err != nil {
			t.Fatalf("seed full leaf row %d: %v", i, err)
		}
	}
	sort.Strings(baseIDs)
	if len(baseIDs) != searchIndexPageRows {
		t.Fatalf("base fixture has %d ids, want %d", len(baseIDs), searchIndexPageRows)
	}
	var trigger capture.SanitizedObservation
	for i := searchIndexPageRows + 1; i < searchIndexPageRows+10000; i++ {
		candidate := indexObservation(t, "editor", "docs", strings.Join(common, " ")+strings.Repeat("?", i))
		if candidate.ContentID() < baseIDs[0] {
			trigger = candidate
			break
		}
	}
	if trigger.ContentID() == "" {
		t.Fatal("could not construct a deterministic insertion before the full leaf's first source ID")
	}
	terms, err := observationSearchTerms(trigger)
	if err != nil {
		t.Fatal(err)
	}
	if len(terms) != search.MaxIndexedObservationTerms {
		t.Fatalf("normalized observation has %d unique text+app+domain terms, want exact max %d", len(terms), search.MaxIndexedObservationTerms)
	}
	if treeCount := len(terms) + 2; treeCount != 130 {
		t.Fatalf("max-term observation touches %d posting trees, want %d including exact app and domain", treeCount, 130)
	}
	if err := forceSynchronizedNonRootSplitFixture(store, base, trigger); err != nil {
		t.Fatalf("prepare all max-term posting trees at the non-root split boundary: %v", err)
	}

	cost, err := estimatedSearchIndexOperations(trigger)
	if err != nil {
		t.Fatal(err)
	}
	if cost >= bolt.MaxTransactionOps || bolt.MaxTransactionOps-cost < 64 {
		t.Fatalf("declared worst-case estimate %d leaves fewer than 64 ops below hard cap %d", cost, bolt.MaxTransactionOps)
	}
	manifestEnvelopeBytes := 0
	if err := store.view(context.Background(), func(reader bolt.Reader) error {
		envelope, found, err := reader.Get(manifestKey)
		if err != nil || !found {
			clear(envelope)
			return fmt.Errorf("read pre-mutation manifest found=%t: %w", found, err)
		}
		manifestEnvelopeBytes = len(envelope)
		clear(envelope)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var counting *operationCountingWriter
	err = store.Update(context.Background(), func(tx *Tx) error {
		counting = &operationCountingWriter{Writer: tx.writer}
		tx.writer = counting
		return tx.PutObservation(trigger)
	})
	if err != nil {
		t.Fatalf("max-term synchronized non-root split failed atomically: %v (attempted %d writer ops; declared bound %d, hard cap %d)", err, counting.operations+1, cost, bolt.MaxTransactionOps)
	}
	operations := counting.operations + 1 // Update loads the manifest before installing the counting wrapper.
	if operations > cost {
		t.Fatalf("max-term synchronized non-root split used %d writer ops, above declared estimate %d", operations, cost)
	}
	if bolt.MaxTransactionOps-operations < 64 {
		t.Fatalf("max-term synchronized non-root split used %d ops, leaving fewer than 64 below hard cap %d", operations, bolt.MaxTransactionOps)
	}
	transactionBytes := counting.bytes + len(manifestKey) + manifestEnvelopeBytes
	if transactionBytes >= bolt.MaxTransactionBytes || bolt.MaxTransactionBytes-transactionBytes < 1<<20 {
		t.Fatalf("max-term synchronized non-root split used %d transaction bytes, want at least 1MiB headroom below hard cap %d", transactionBytes, bolt.MaxTransactionBytes)
	}
	t.Logf("max-term synchronized non-root split touched 130 trees: ops=%d estimate=%d op-cap=%d bytes=%d byte-cap=%d", operations, cost, bolt.MaxTransactionOps, transactionBytes, bolt.MaxTransactionBytes)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatalf("reopen with pending max-130 sibling links: %v", err)
	}
	defer store.Close()
	// Before repairing the pending parent links, prove that a reopened reader
	// can cross each encrypted B-link and still report truncation accurately.
	linkedResponse, err := search.New(store).Search(context.Background(), search.Request{
		Query: "shared000", AppID: "editor", Domain: "docs", ResultLimit: 1,
		SnippetBytes: 64, ResponseBytes: 4096, ScanLimit: 1,
	})
	if err != nil || len(linkedResponse.Results) != 1 || linkedResponse.Results[0].SourceID != trigger.ContentID() || !linkedResponse.Truncated {
		t.Fatalf("search through reopened max-130 pending links results=%+v truncated=%t err=%v", linkedResponse.Results, linkedResponse.Truncated, err)
	}
	trees, err := synchronizedSplitTrees(store, trigger)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for i := range trees {
			clear(trees[i].digest[:])
		}
	}()
	if err := forceRightSiblingRepairFixture(store, trees); err != nil {
		t.Fatalf("prepare the bounded right-sibling repair leaves: %v", err)
	}
	triggerRight, err := observationAfterSynchronizedPivot(t, common, trees[0], store)
	if err != nil {
		t.Fatal(err)
	}
	repairCost, err := estimatedSearchIndexOperations(triggerRight)
	if err != nil {
		t.Fatal(err)
	}
	repairManifestBytes, err := readManifestEnvelopeBytes(store)
	if err != nil {
		t.Fatal(err)
	}
	var repairCounting *operationCountingWriter
	err = store.Update(context.Background(), func(tx *Tx) error {
		repairCounting = &operationCountingWriter{Writer: tx.writer}
		tx.writer = repairCounting
		return tx.PutObservation(triggerRight)
	})
	if err != nil {
		t.Fatalf("ordinary max-term write repairing 130 sibling links: %v (attempted %d ops)", err, repairCounting.operations+1)
	}
	repairOps := repairCounting.operations + 1
	if repairOps > repairCost || bolt.MaxTransactionOps-repairOps < 64 {
		t.Fatalf("ordinary max-term sibling repair used %d ops, estimate=%d cap=%d", repairOps, repairCost, bolt.MaxTransactionOps)
	}
	repairBytes := repairCounting.bytes + len(manifestKey) + repairManifestBytes
	if repairBytes >= bolt.MaxTransactionBytes || bolt.MaxTransactionBytes-repairBytes < 1<<20 {
		t.Fatalf("ordinary max-term sibling repair used %d bytes, want at least 1MiB headroom below %d", repairBytes, bolt.MaxTransactionBytes)
	}
	t.Logf("max-130 ordinary sibling repair: ops=%d estimate=%d op-cap=%d bytes=%d byte-cap=%d", repairOps, repairCost, bolt.MaxTransactionOps, repairBytes, bolt.MaxTransactionBytes)
	var root [sha256.Size]byte
	if err := store.view(context.Background(), func(reader bolt.Reader) error {
		rootNode, found, err := readSearchIndexTreeNodeFromReader(reader, store, trees[0].kind, trees[0].digest, root)
		if err != nil || !found || rootNode.leaf || len(rootNode.children) != 2 {
			return fmt.Errorf("repaired root found=%t leaf=%t children=%d err=%v", found, rootNode.leaf, len(rootNode.children), err)
		}
		left, found, err := readSearchIndexTreeNodeFromReader(reader, store, trees[0].kind, trees[0].digest, rootNode.children[0].id)
		if err != nil || !found || left.rightSibling != rootNode.children[1].id || rootNode.children[0].upper != left.siblingPivot {
			return fmt.Errorf("root does not publish the deferred link: found=%t err=%v", found, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify repaired 130-tree root links: %v", err)
	}
	response, err := search.New(store).Search(context.Background(), search.Request{
		Query: "shared000", AppID: "editor", Domain: "docs", ResultLimit: 1,
		SnippetBytes: 64, ResponseBytes: 4096, ScanLimit: 1,
	})
	if err != nil || len(response.Results) != 1 || response.Results[0].SourceID != trigger.ContentID() {
		t.Fatalf("query after max-130 sibling repair results=%+v err=%v", response.Results, err)
	}
	deleteCost, err := estimatedSearchIndexOperations(triggerRight)
	if err != nil {
		t.Fatal(err)
	}
	deleteManifestBytes, err := readManifestEnvelopeBytes(store)
	if err != nil {
		t.Fatal(err)
	}
	var deleteCounting *operationCountingWriter
	err = store.Update(context.Background(), func(tx *Tx) error {
		deleteCounting = &operationCountingWriter{Writer: tx.writer}
		tx.writer = deleteCounting
		return tx.deleteObservation(triggerRight.ContentID())
	})
	if err != nil {
		t.Fatalf("delete from the repaired right subtree: %v", err)
	}
	deleteOps := deleteCounting.operations + 1
	deleteBytes := deleteCounting.bytes + len(manifestKey) + deleteManifestBytes
	if deleteOps > deleteCost || bolt.MaxTransactionOps-deleteOps < 64 || deleteBytes >= bolt.MaxTransactionBytes || bolt.MaxTransactionBytes-deleteBytes < 1<<20 {
		t.Fatalf("max-130 repaired-subtree deletion ops=%d estimate=%d bytes=%d", deleteOps, deleteCost, deleteBytes)
	}
	t.Logf("max-130 repaired-subtree deletion: ops=%d estimate=%d op-cap=%d bytes=%d byte-cap=%d", deleteOps, deleteCost, bolt.MaxTransactionOps, deleteBytes, bolt.MaxTransactionBytes)
}

func TestDurableIndexMaxTermRepairConcurrentWithLeafSplitStaysBounded(t *testing.T) {
	path := t.TempDir() + "/search.db"
	key := durableTestKey(t, 0x92)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	common := make([]string, 126)
	for i := range common {
		common[i] = fmt.Sprintf("cascade%03d", i)
	}
	base := make([]capture.SanitizedObservation, 2*searchIndexPageRows)
	baseIDs := make([]string, len(base))
	for i := range base {
		base[i] = indexObservation(t, "editor", "docs", strings.Join(common, " ")+strings.Repeat("!", i+1))
		baseIDs[i] = base[i].ContentID()
		if err := store.PutObservation(context.Background(), base[i]); err != nil {
			t.Fatalf("seed full-leaf row %d: %v", i, err)
		}
	}
	sort.Strings(baseIDs)
	textPrefix := strings.Join(common, " ")
	var triggerBefore capture.SanitizedObservation
	for suffix := len(base) + 1; suffix < 3000; suffix++ {
		candidate := indexObservation(t, "editor", "docs", textPrefix+strings.Repeat("?", suffix))
		if candidate.ContentID() < baseIDs[0] {
			triggerBefore = candidate
			break
		}
	}
	if triggerBefore.ContentID() == "" {
		t.Fatal("could not create an observation ordered before the first full leaf")
	}
	trees, err := synchronizedSplitTrees(store, triggerBefore)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for i := range trees {
			clear(trees[i].digest[:])
		}
	}()
	if len(trees) != 130 {
		t.Fatalf("max-term cascade fixture touches %d trees, want 130", len(trees))
	}
	if err := forceSynchronizedTwoFullLeafBranch(store, base, trees); err != nil {
		t.Fatalf("install two-full-leaf branch fixture: %v", err)
	}
	groupTwo := append([]capture.SanitizedObservation(nil), base...)
	sort.Slice(groupTwo, func(i, j int) bool { return groupTwo[i].ContentID() < groupTwo[j].ContentID() })
	var triggerInside capture.SanitizedObservation
	for suffix := 1; suffix < 3000; suffix++ {
		candidate := indexObservation(t, "editor", "docs", textPrefix+strings.Repeat("@", suffix))
		if candidate.ContentID() > groupTwo[searchIndexPageRows].ContentID() && candidate.ContentID() < groupTwo[len(groupTwo)-1].ContentID() {
			triggerInside = candidate
			break
		}
	}
	if triggerInside.ContentID() == "" {
		t.Fatal("could not create an observation inside the second full leaf's source-ID range")
	}
	beforeCost, err := estimatedSearchIndexOperations(triggerBefore)
	if err != nil {
		t.Fatal(err)
	}
	beforeManifestBytes, err := readManifestEnvelopeBytes(store)
	if err != nil {
		t.Fatal(err)
	}
	var beforeCounting *operationCountingWriter
	if err := store.Update(context.Background(), func(tx *Tx) error {
		beforeCounting = &operationCountingWriter{Writer: tx.writer}
		tx.writer = beforeCounting
		return tx.PutObservation(triggerBefore)
	}); err != nil {
		t.Fatalf("publish pending max-130 branch split: %v (attempted %d ops)", err, beforeCounting.operations+1)
	}
	beforeOps := beforeCounting.operations + 1
	beforeBytes := beforeCounting.bytes + len(manifestKey) + beforeManifestBytes
	if beforeOps > beforeCost || bolt.MaxTransactionOps-beforeOps < 64 {
		t.Fatalf("deferred split used %d ops, want at least 64-op headroom", beforeCounting.operations+1)
	}
	if beforeBytes >= bolt.MaxTransactionBytes || bolt.MaxTransactionBytes-beforeBytes < 1<<20 {
		t.Fatalf("deferred split used %d bytes, want at least 1MiB byte headroom", beforeBytes)
	}
	t.Logf("max-130 deferred split setup: ops=%d estimate=%d bytes=%d", beforeOps, beforeCost, beforeBytes)
	// A linked branch with a full target leaf receives both the leaf split and
	// the parent-link repair in one transaction, across all 130 exact trees.
	cost, err := estimatedSearchIndexOperations(triggerInside)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := readManifestEnvelopeBytes(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(tx *Tx) error {
		if err := tx.PutObservation(triggerInside); err != nil {
			return err
		}
		return errors.New("rollback concurrent sibling repair and leaf split")
	}); !errors.Is(err, ErrTransaction) {
		t.Fatalf("concurrent repair+split rollback error=%v, want ErrTransaction", err)
	}
	if _, found, err := store.GetObservation(context.Background(), triggerInside.ContentID()); err != nil || found {
		t.Fatalf("rolled-back concurrent repair stored observation: found=%t err=%v", found, err)
	}
	var counting *operationCountingWriter
	err = store.Update(context.Background(), func(tx *Tx) error {
		counting = &operationCountingWriter{Writer: tx.writer}
		tx.writer = counting
		return tx.PutObservation(triggerInside)
	})
	if err != nil {
		t.Fatalf("repair plus full-leaf split failed: %v (attempted %d ops estimate %d)", err, counting.operations+1, cost)
	}
	operations := counting.operations + 1
	bytesUsed := counting.bytes + len(manifestKey) + manifestBytes
	if operations > cost || bolt.MaxTransactionOps-operations < 64 {
		t.Fatalf("repair+leaf split used %d ops, estimate=%d cap=%d", operations, cost, bolt.MaxTransactionOps)
	}
	if bytesUsed >= bolt.MaxTransactionBytes || bolt.MaxTransactionBytes-bytesUsed < 1<<20 {
		t.Fatalf("repair+leaf split used %d bytes, want 1MiB headroom under %d", bytesUsed, bolt.MaxTransactionBytes)
	}
	t.Logf("max-130 repair concurrent with full-leaf split: ops=%d estimate=%d op-cap=%d bytes=%d byte-cap=%d", operations, cost, bolt.MaxTransactionOps, bytesUsed, bolt.MaxTransactionBytes)
	var root [sha256.Size]byte
	if err := store.view(context.Background(), func(reader bolt.Reader) error {
		rootNode, found, err := readSearchIndexTreeNodeFromReader(reader, store, trees[0].kind, trees[0].digest, root)
		if err != nil || !found || rootNode.leaf || len(rootNode.children) != 2 {
			return fmt.Errorf("concurrent repair root found=%t leaf=%t children=%d err=%v", found, rootNode.leaf, len(rootNode.children), err)
		}
		left, found, err := readSearchIndexTreeNodeFromReader(reader, store, trees[0].kind, trees[0].digest, rootNode.children[0].id)
		if err != nil || !found || left.rightSibling != ([sha256.Size]byte{}) || rootNode.children[0].upper != left.children[len(left.children)-1].upper {
			return fmt.Errorf("concurrent repair did not replace deferred link with a parent pointer: found=%t err=%v leftLink=%x rootRight=%x leftUpper=%x childUpper=%x", found, err, left.rightSibling[:4], rootNode.children[1].id[:4], rootNode.children[0].upper[:5], left.children[len(left.children)-1].upper[:5])
		}
		ordered, err := searchIndexOrderKey(trees[0].kind, 1, triggerInside.ContentID())
		if err != nil {
			return err
		}
		var wanted [searchIndexTreeOrderBytes]byte
		copy(wanted[:], ordered)
		clear(ordered)
		child := left.children[treeChildPosition(left.children, wanted)]
		leaf, found, err := readSearchIndexTreeNodeFromReader(reader, store, trees[0].kind, trees[0].digest, child.id)
		if err != nil || !found || !leaf.leaf {
			return fmt.Errorf("post-split target leaf found=%t err=%v", found, err)
		}
		for _, posting := range leaf.postings {
			if posting.sourceID == triggerInside.ContentID() {
				return nil
			}
		}
		return errors.New("concurrent repair lost the inserted posting")
	}); err != nil {
		t.Fatalf("verify root repair and leaf split: %v", err)
	}
	response, err := search.New(store).Search(context.Background(), search.Request{
		Query: "cascade000", AppID: "editor", Domain: "docs", ResultLimit: 1,
		SnippetBytes: 64, ResponseBytes: 4096, ScanLimit: 1,
	})
	if err != nil || len(response.Results) != 1 || response.Results[0].SourceID != triggerBefore.ContentID() {
		t.Fatalf("search after concurrent repair+split results=%+v err=%v", response.Results, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatalf("reopen after concurrent repair+split: %v", err)
	}
	response, err = search.New(store).Search(context.Background(), search.Request{
		Query: "cascade000", AppID: "editor", Domain: "docs", ResultLimit: 1,
		SnippetBytes: 64, ResponseBytes: 4096, ScanLimit: 1,
	})
	if err != nil || len(response.Results) != 1 || response.Results[0].SourceID != triggerBefore.ContentID() {
		t.Fatalf("reopened search after concurrent repair+split results=%+v err=%v", response.Results, err)
	}
}

func TestSearchIndexPendingSiblingIsRepairedBeforeLaterSplit(t *testing.T) {
	store, err := OpenDurable(t.TempDir()+"/search.db", durableTestKey(t, 0x93), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const kind = searchIndexTerm
	digest := store.searchLookupDigest(kind, "bounded-sibling-chain")
	defer clear(digest[:])
	var root [sha256.Size]byte
	branchID, err := newSearchIndexTreeNodeID()
	if err != nil {
		t.Fatal(err)
	}
	firstLeafID, err := newSearchIndexTreeNodeID()
	if err != nil {
		t.Fatal(err)
	}
	deferredRightID, err := newSearchIndexTreeNodeID()
	if err != nil {
		t.Fatal(err)
	}
	deferredRightLeafID, err := newSearchIndexTreeNodeID()
	if err != nil {
		t.Fatal(err)
	}
	orderFor := func(n int) [searchIndexTreeOrderBytes]byte {
		t.Helper()
		encoded, err := searchIndexOrderKey(kind, 1, fmt.Sprintf("%064x", n))
		if err != nil {
			t.Fatal(err)
		}
		var ordered [searchIndexTreeOrderBytes]byte
		copy(ordered[:], encoded)
		clear(encoded)
		return ordered
	}
	children := make([]searchIndexTreeChild, searchIndexTreeMaxChildren)
	for i := range children {
		children[i].upper = orderFor(searchIndexPageRows + i)
		children[i].id = store.searchIndexTreeChildID(kind, digest, branchID, children[i].upper, searchIndexTreeLeaf, byte(i&1))
	}
	firstLeafID = children[0].id
	firstPostings := make([]searchIndexPosting, searchIndexPageRows)
	for i := range firstPostings {
		firstPostings[i] = searchIndexPosting{sourceID: fmt.Sprintf("%064x", i+1), fields: 1}
	}
	deferredRight := searchIndexTreeNode{
		children: []searchIndexTreeChild{{upper: searchIndexTreeInfinity, id: deferredRightLeafID}},
	}
	branch := searchIndexTreeNode{
		children:     children,
		rightSibling: deferredRightID,
		siblingPivot: children[len(children)-1].upper,
	}
	if err := store.Update(context.Background(), func(tx *Tx) error {
		if err := tx.putSearchIndexTreeNodeLocked(kind, digest, firstLeafID, searchIndexTreeNode{leaf: true, postings: firstPostings}, nil); err != nil {
			return err
		}
		if err := tx.putSearchIndexTreeNodeLocked(kind, digest, children[1].id, searchIndexTreeNode{leaf: true}, nil); err != nil {
			return err
		}
		if err := tx.putSearchIndexTreeNodeLocked(kind, digest, deferredRightLeafID, searchIndexTreeNode{leaf: true}, nil); err != nil {
			return err
		}
		if err := tx.putSearchIndexTreeNodeLocked(kind, digest, deferredRightID, deferredRight, nil); err != nil {
			return err
		}
		if err := tx.putSearchIndexTreeNodeLocked(kind, digest, branchID, branch, nil); err != nil {
			return err
		}
		return tx.putSearchIndexTreeNodeLocked(kind, digest, root, searchIndexTreeNode{
			children: []searchIndexTreeChild{{upper: searchIndexTreeInfinity, id: branchID}},
		}, nil)
	}); err != nil {
		t.Fatalf("install full branch with one pending sibling link: %v", err)
	}

	newID := fmt.Sprintf("%064x", 0)
	newOrder, err := searchIndexOrderKey(kind, 1, newID)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(newOrder)
	var rejected *operationCountingWriter
	var splitErr error
	err = store.Update(context.Background(), func(tx *Tx) error {
		rejected = &operationCountingWriter{Writer: tx.writer}
		tx.writer = rejected
		splitErr = tx.insertSearchIndexTreeLocked(kind, digest, newOrder, searchIndexPosting{sourceID: newID, fields: 1})
		return splitErr
	})
	if !errors.Is(splitErr, search.ErrIndexBudget) || !errors.Is(err, ErrTransaction) {
		t.Fatalf("second split while the maximum branch has an unrepaired sibling inner=%v outer=%v, want ErrIndexBudget / ErrTransaction", splitErr, err)
	}
	if rejected.operations >= bolt.MaxTransactionOps || rejected.bytes >= bolt.MaxTransactionBytes {
		t.Fatalf("fail-closed second split exceeded transaction bounds: ops=%d bytes=%d", rejected.operations, rejected.bytes)
	}
	if err := store.view(context.Background(), func(reader bolt.Reader) error {
		rootNode, found, err := readSearchIndexTreeNodeFromReader(reader, store, kind, digest, root)
		if err != nil || !found || len(rootNode.children) != 1 || rootNode.children[0].id != branchID {
			return fmt.Errorf("rejected split changed root: found=%t err=%v children=%d", found, err, len(rootNode.children))
		}
		left, found, err := readSearchIndexTreeNodeFromReader(reader, store, kind, digest, branchID)
		if err != nil || !found || len(left.children) != searchIndexTreeMaxChildren || left.rightSibling != deferredRightID {
			return fmt.Errorf("rejected split changed pending branch: found=%t err=%v children=%d", found, err, len(left.children))
		}
		leaf, found, err := readSearchIndexTreeNodeFromReader(reader, store, kind, digest, firstLeafID)
		if err != nil || !found || len(leaf.postings) != searchIndexPageRows {
			return fmt.Errorf("rejected split changed full leaf: found=%t err=%v rows=%d", found, err, len(leaf.postings))
		}
		for _, posting := range leaf.postings {
			if posting.sourceID == newID {
				return errors.New("rejected split left a partially written posting")
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("rejected branch split was not atomic: %v", err)
	}

	// A non-splitting write repairs the parent before a later split is allowed.
	repairID := fmt.Sprintf("%064x", searchIndexPageRows+1)
	repairOrder, err := searchIndexOrderKey(kind, 1, repairID)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(repairOrder)
	if err := store.Update(context.Background(), func(tx *Tx) error {
		return tx.insertSearchIndexTreeLocked(kind, digest, repairOrder, searchIndexPosting{sourceID: repairID, fields: 1})
	}); err != nil {
		t.Fatalf("non-splitting mutation did not repair parent link: %v", err)
	}
	if err := store.view(context.Background(), func(reader bolt.Reader) error {
		rootNode, found, err := readSearchIndexTreeNodeFromReader(reader, store, kind, digest, root)
		if err != nil || !found || len(rootNode.children) != 2 || rootNode.children[0].upper != children[len(children)-1].upper || rootNode.children[1].id != deferredRightID {
			return fmt.Errorf("ordinary mutation did not publish repaired parent link: found=%t err=%v children=%d", found, err, len(rootNode.children))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(tx *Tx) error {
		return tx.insertSearchIndexTreeLocked(kind, digest, newOrder, searchIndexPosting{sourceID: newID, fields: 1})
	}); err != nil {
		t.Fatalf("later branch split after repair: %v", err)
	}
	if err := store.view(context.Background(), func(reader bolt.Reader) error {
		rootNode, found, err := readSearchIndexTreeNodeFromReader(reader, store, kind, digest, root)
		if err != nil || !found || len(rootNode.children) != 2 {
			return fmt.Errorf("later split changed repaired root unexpectedly: found=%t err=%v children=%d", found, err, len(rootNode.children))
		}
		left, found, err := readSearchIndexTreeNodeFromReader(reader, store, kind, digest, rootNode.children[0].id)
		if err != nil || !found || left.rightSibling == ([sha256.Size]byte{}) || bytes.Compare(left.siblingPivot[:], rootNode.children[0].upper[:]) >= 0 {
			return fmt.Errorf("later split did not publish one bounded deferred link: found=%t err=%v", found, err)
		}
		right, found, err := readSearchIndexTreeNodeFromReader(reader, store, kind, digest, left.rightSibling)
		if err != nil || !found || right.leaf || right.rightSibling != ([sha256.Size]byte{}) {
			return fmt.Errorf("later split created a sibling chain: found=%t err=%v leaf=%t next=%x", found, err, right.leaf, right.rightSibling[:4])
		}
		return nil
	}); err != nil {
		t.Fatalf("verify later split's bounded sibling chain: %v", err)
	}
}

type synchronizedSplitTree struct {
	kind   byte
	digest [sha256.Size]byte
	root   [sha256.Size]byte
	term   string
}

func synchronizedSplitTrees(store *Store, observation capture.SanitizedObservation) ([]synchronizedSplitTree, error) {
	terms, err := observationSearchTerms(observation)
	if err != nil {
		return nil, err
	}
	treeSet := make([]synchronizedSplitTree, 0, len(terms)+2)
	for _, term := range sortedTermKeys(terms) {
		treeSet = append(treeSet, synchronizedSplitTree{kind: searchIndexTerm, digest: store.searchLookupDigest(searchIndexTerm, term), term: term})
	}
	if appID := observation.AppID(); appID != "" {
		treeSet = append(treeSet, synchronizedSplitTree{kind: searchIndexApp, digest: store.searchLookupDigest(searchIndexApp, appID)})
	}
	if domain := observation.Domain(); domain != "" {
		treeSet = append(treeSet, synchronizedSplitTree{kind: searchIndexDomain, digest: store.searchLookupDigest(searchIndexDomain, domain)})
	}
	return treeSet, nil
}

func forceRightSiblingRepairFixture(store *Store, trees []synchronizedSplitTree) error {
	var root [sha256.Size]byte
	return store.Update(context.Background(), func(tx *Tx) error {
		for _, tree := range trees {
			rootNode, rootEnvelope, found, err := tx.readSearchIndexTreeNodeLocked(tree.kind, tree.digest, root)
			if err != nil || !found || rootNode.leaf || len(rootNode.children) != 1 {
				clear(rootEnvelope)
				if err != nil {
					return err
				}
				return fmt.Errorf("pending-link root found=%t leaf=%t children=%d", found, rootNode.leaf, len(rootNode.children))
			}
			clear(rootEnvelope)
			leftID := rootNode.children[0].id
			left, leftEnvelope, found, err := tx.readSearchIndexTreeNodeLocked(tree.kind, tree.digest, leftID)
			if err != nil || !found || left.rightSibling == ([sha256.Size]byte{}) {
				clear(leftEnvelope)
				if err != nil {
					return err
				}
				return fmt.Errorf("pending left branch found=%t sibling=%x", found, left.rightSibling[:4])
			}
			clear(leftEnvelope)
			rightID := left.rightSibling
			right, rightEnvelope, found, err := tx.readSearchIndexTreeNodeLocked(tree.kind, tree.digest, rightID)
			if err != nil || !found || right.leaf || len(right.children) == 0 {
				clear(rightEnvelope)
				if err != nil {
					return err
				}
				return fmt.Errorf("pending right branch found=%t leaf=%t children=%d", found, right.leaf, len(right.children))
			}
			leafID := right.children[0].id
			if err := tx.putSearchIndexTreeNodeLocked(tree.kind, tree.digest, rightID, searchIndexTreeNode{
				children: []searchIndexTreeChild{{upper: searchIndexTreeInfinity, id: leafID}},
			}, rightEnvelope); err != nil {
				clear(rightEnvelope)
				return err
			}
			clear(rightEnvelope)
			if err := tx.putSearchIndexTreeNodeLocked(tree.kind, tree.digest, leafID, searchIndexTreeNode{leaf: true}, nil); err != nil {
				return err
			}
		}
		return nil
	})
}

func forceSynchronizedTwoFullLeafBranch(store *Store, observations []capture.SanitizedObservation, trees []synchronizedSplitTree) error {
	if len(observations) != 2*searchIndexPageRows {
		return fmt.Errorf("two-leaf fixture has %d observations, want %d", len(observations), 2*searchIndexPageRows)
	}
	ordered := append([]capture.SanitizedObservation(nil), observations...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ContentID() < ordered[j].ContentID() })
	for start := 0; start < len(trees); start += 8 {
		end := min(start+8, len(trees))
		if err := store.Update(context.Background(), func(tx *Tx) error {
			for _, tree := range trees[start:end] {
				var root [sha256.Size]byte
				rootNode, rootEnvelope, found, err := tx.readSearchIndexTreeNodeLocked(tree.kind, tree.digest, root)
				if err != nil || !found {
					clear(rootEnvelope)
					if err != nil {
						return err
					}
					return fmt.Errorf("missing old root for term %q", tree.term)
				}
				if rootNode.leaf {
					clear(rootEnvelope)
					return fmt.Errorf("old tree for term %q is unexpectedly a leaf", tree.term)
				}
				for _, child := range rootNode.children {
					if err := deleteSearchIndexTreeSubtreeLocked(tx, tree, child.id, 1); err != nil {
						clear(rootEnvelope)
						return err
					}
				}
				if rootNode.rightSibling != ([sha256.Size]byte{}) {
					if err := deleteSearchIndexTreeSubtreeLocked(tx, tree, rootNode.rightSibling, 1); err != nil {
						clear(rootEnvelope)
						return err
					}
				}
				groups := [2][]searchIndexPosting{make([]searchIndexPosting, 0, searchIndexPageRows), make([]searchIndexPosting, 0, searchIndexPageRows)}
				for i, observation := range ordered {
					fields := byte(0)
					if tree.kind == searchIndexTerm {
						terms, err := observationSearchTerms(observation)
						if err != nil {
							clear(rootEnvelope)
							return err
						}
						fields = terms[tree.term]
						if fields == 0 {
							clear(rootEnvelope)
							return fmt.Errorf("term %q missing from fixture observation %d", tree.term, i)
						}
					}
					group := 0
					if i >= searchIndexPageRows {
						group = 1
					}
					groups[group] = append(groups[group], searchIndexPosting{sourceID: observation.ContentID(), fields: fields})
				}
				for i := range groups {
					sortSearchIndexPostings(groups[i])
				}
				leafIDs := [2][sha256.Size]byte{}
				for i := range leafIDs {
					leafIDs[i], err = newSearchIndexTreeNodeID()
					if err != nil {
						clear(rootEnvelope)
						return err
					}
				}
				branchID, err := newSearchIndexTreeNodeID()
				if err != nil {
					clear(rootEnvelope)
					return err
				}
				orderFor := func(posting searchIndexPosting) ([searchIndexTreeOrderBytes]byte, error) {
					var result [searchIndexTreeOrderBytes]byte
					value, err := searchIndexOrderKey(tree.kind, posting.fields, posting.sourceID)
					copy(result[:], value)
					clear(value)
					return result, err
				}
				firstUpper, err := orderFor(groups[0][len(groups[0])-1])
				if err != nil {
					clear(rootEnvelope)
					return err
				}
				secondFirst, err := orderFor(groups[1][0])
				if err != nil {
					clear(rootEnvelope)
					return err
				}
				secondUpper, err := orderFor(groups[1][len(groups[1])-1])
				if err != nil {
					clear(rootEnvelope)
					return err
				}
				children := make([]searchIndexTreeChild, searchIndexTreeMaxChildren)
				children[0] = searchIndexTreeChild{upper: firstUpper, id: leafIDs[0]}
				for i := 1; i < 100; i++ {
					sourceID, err := incrementHexID(groups[0][len(groups[0])-1].sourceID, i)
					if err != nil {
						clear(rootEnvelope)
						return err
					}
					upper, err := searchIndexOrderKey(tree.kind, groups[0][len(groups[0])-1].fields, sourceID)
					if err != nil {
						clear(rootEnvelope)
						return err
					}
					copy(children[i].upper[:], upper)
					clear(upper)
					children[i].id = store.searchIndexTreeChildID(tree.kind, tree.digest, branchID, children[i].upper, searchIndexTreeLeaf, byte(i&1))
				}
				if bytes.Compare(children[99].upper[:], secondFirst[:]) >= 0 {
					clear(rootEnvelope)
					return errors.New("synthetic first-range fence overlaps the second full leaf")
				}
				children[100] = searchIndexTreeChild{upper: secondUpper, id: leafIDs[1]}
				for i := 101; i < len(children)-1; i++ {
					sourceID, err := incrementHexID(groups[1][len(groups[1])-1].sourceID, i-100)
					if err != nil {
						clear(rootEnvelope)
						return err
					}
					upper, err := searchIndexOrderKey(tree.kind, groups[1][len(groups[1])-1].fields, sourceID)
					if err != nil {
						clear(rootEnvelope)
						return err
					}
					copy(children[i].upper[:], upper)
					clear(upper)
					children[i].id = store.searchIndexTreeChildID(tree.kind, tree.digest, branchID, children[i].upper, searchIndexTreeLeaf, byte(i&1))
				}
				children[len(children)-1] = searchIndexTreeChild{
					upper: searchIndexTreeInfinity,
					id:    store.searchIndexTreeChildID(tree.kind, tree.digest, branchID, searchIndexTreeInfinity, searchIndexTreeLeaf, 1),
				}
				for i := range groups {
					if err := tx.putSearchIndexTreeNodeLocked(tree.kind, tree.digest, leafIDs[i], searchIndexTreeNode{leaf: true, postings: groups[i]}, nil); err != nil {
						clear(rootEnvelope)
						return err
					}
				}
				if err := tx.putSearchIndexTreeNodeLocked(tree.kind, tree.digest, branchID, searchIndexTreeNode{children: children}, nil); err != nil {
					clear(rootEnvelope)
					return err
				}
				if err := tx.putSearchIndexTreeNodeLocked(tree.kind, tree.digest, root, searchIndexTreeNode{
					children: []searchIndexTreeChild{{upper: searchIndexTreeInfinity, id: branchID}},
				}, rootEnvelope); err != nil {
					clear(rootEnvelope)
					return err
				}
				clear(rootEnvelope)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("install synchronized branch fixtures [%d,%d): %w", start, end, err)
		}
	}
	return nil
}

func deleteSearchIndexTreeSubtreeLocked(tx *Tx, tree synchronizedSplitTree, nodeID [sha256.Size]byte, depth int) error {
	if depth >= searchIndexTreeMaxDepth {
		return ErrCorrupt
	}
	node, envelope, found, err := tx.readSearchIndexTreeNodeLocked(tree.kind, tree.digest, nodeID)
	if err != nil || !found {
		clear(envelope)
		if err != nil {
			return err
		}
		return ErrCorrupt
	}
	if !node.leaf {
		for _, child := range node.children {
			if err := deleteSearchIndexTreeSubtreeLocked(tx, tree, child.id, depth+1); err != nil {
				clear(envelope)
				return err
			}
		}
		if node.rightSibling != ([sha256.Size]byte{}) {
			if err := deleteSearchIndexTreeSubtreeLocked(tx, tree, node.rightSibling, depth); err != nil {
				clear(envelope)
				return err
			}
		}
	}
	key := tx.store.searchIndexTreeKey(tree.kind, tree.digest, nodeID)
	err = tx.deleteKnownEntryLocked(key, envelope)
	clear(key)
	clear(envelope)
	return err
}

func observationAfterSynchronizedPivot(t *testing.T, common []string, tree synchronizedSplitTree, store *Store) (capture.SanitizedObservation, error) {
	t.Helper()
	var pivotID string
	var root [sha256.Size]byte
	if err := store.view(context.Background(), func(reader bolt.Reader) error {
		rootNode, found, err := readSearchIndexTreeNodeFromReader(reader, store, tree.kind, tree.digest, root)
		if err != nil || !found || rootNode.leaf || len(rootNode.children) != 1 {
			return fmt.Errorf("read pending-link root found=%t err=%v", found, err)
		}
		left, found, err := readSearchIndexTreeNodeFromReader(reader, store, tree.kind, tree.digest, rootNode.children[0].id)
		if err != nil || !found || left.rightSibling == ([sha256.Size]byte{}) {
			return fmt.Errorf("read pending-link left branch found=%t err=%v", found, err)
		}
		pivotID = hex.EncodeToString(left.siblingPivot[1:])
		return nil
	}); err != nil {
		return capture.SanitizedObservation{}, err
	}
	base := strings.Join(common, " ")
	for suffix := 65; suffix < 3000; suffix++ {
		candidate, err := capture.SanitizeObservation("editor", "docs", base+strings.Repeat("@", suffix), 4096)
		if err != nil {
			return capture.SanitizedObservation{}, err
		}
		if candidate.ContentID() > pivotID {
			return candidate, nil
		}
	}
	return capture.SanitizedObservation{}, errors.New("could not find max-term observation after the synchronized sibling pivot")
}

func readSearchIndexTreeNodeFromReader(reader bolt.Reader, store *Store, kind byte, digest, nodeID [sha256.Size]byte) (searchIndexTreeNode, bool, error) {
	key := store.searchIndexTreeKey(kind, digest, nodeID)
	envelope, found, err := reader.Get(key)
	if err != nil || !found {
		clear(key)
		clear(envelope)
		return searchIndexTreeNode{}, found, err
	}
	plain, err := store.open(key, "search-index-tree-v4", envelope)
	clear(key)
	clear(envelope)
	if err != nil {
		clear(plain)
		return searchIndexTreeNode{}, false, err
	}
	node, err := decodeSearchIndexTreeNode(plain, kind)
	clear(plain)
	return node, err == nil, err
}

func readManifestEnvelopeBytes(store *Store) (int, error) {
	valueBytes := 0
	err := store.view(context.Background(), func(reader bolt.Reader) error {
		envelope, found, err := reader.Get(manifestKey)
		if err != nil || !found {
			clear(envelope)
			return fmt.Errorf("read manifest found=%t: %w", found, err)
		}
		valueBytes = len(envelope)
		clear(envelope)
		return nil
	})
	return valueBytes, err
}

func forceSynchronizedNonRootSplitFixture(store *Store, observations []capture.SanitizedObservation, trigger capture.SanitizedObservation) error {
	terms, err := observationSearchTerms(trigger)
	if err != nil {
		return err
	}
	treeSet := make([]synchronizedSplitTree, 0, len(terms)+2)
	for _, term := range sortedTermKeys(terms) {
		treeSet = append(treeSet, synchronizedSplitTree{kind: searchIndexTerm, digest: store.searchLookupDigest(searchIndexTerm, term), term: term})
	}
	appID, domain := trigger.AppID(), trigger.Domain()
	treeSet = append(treeSet,
		synchronizedSplitTree{kind: searchIndexApp, digest: store.searchLookupDigest(searchIndexApp, appID)},
		synchronizedSplitTree{kind: searchIndexDomain, digest: store.searchLookupDigest(searchIndexDomain, domain)},
	)
	if len(treeSet) != 130 {
		return fmt.Errorf("fixture has %d posting trees, want 130", len(treeSet))
	}
	baseIDs := make([]string, len(observations))
	for i, observation := range observations {
		baseIDs[i] = observation.ContentID()
	}
	sort.Strings(baseIDs)
	if len(baseIDs) != searchIndexPageRows || trigger.ContentID() >= baseIDs[0] {
		return fmt.Errorf("fixture source ordering is invalid: %d base IDs, trigger=%s min=%s", len(baseIDs), trigger.ContentID(), baseIDs[0])
	}
	return store.Update(context.Background(), func(tx *Tx) error {
		for _, tree := range treeSet {
			rootNode, rootEnvelope, found, err := tx.readSearchIndexTreeNodeLocked(tree.kind, tree.digest, tree.root)
			if err != nil || !found {
				clear(rootEnvelope)
				if err != nil {
					return err
				}
				return fmt.Errorf("missing posting root for kind %d", tree.kind)
			}
			if !rootNode.leaf || len(rootNode.postings) != searchIndexPageRows {
				clear(rootEnvelope)
				return fmt.Errorf("kind %d root has leaf=%t rows=%d; want full root leaf", tree.kind, rootNode.leaf, len(rootNode.postings))
			}
			for i, posting := range rootNode.postings {
				if posting.sourceID != baseIDs[i] {
					clear(rootEnvelope)
					return fmt.Errorf("kind %d posting %d = %s, want %s", tree.kind, i, posting.sourceID, baseIDs[i])
				}
			}
			branchID, err := newSearchIndexTreeNodeID()
			if err != nil {
				clear(rootEnvelope)
				return err
			}
			leafID, err := newSearchIndexTreeNodeID()
			if err != nil {
				clear(rootEnvelope)
				return err
			}
			lastOrder, err := searchIndexOrderKey(tree.kind, rootNode.postings[len(rootNode.postings)-1].fields, baseIDs[len(baseIDs)-1])
			if err != nil {
				clear(rootEnvelope)
				return err
			}
			var last [searchIndexTreeOrderBytes]byte
			copy(last[:], lastOrder)
			clear(lastOrder)
			children := make([]searchIndexTreeChild, searchIndexTreeMaxChildren)
			children[0] = searchIndexTreeChild{upper: last, id: leafID}
			for i := 1; i < len(children)-1; i++ {
				id, err := incrementHexID(baseIDs[len(baseIDs)-1], i)
				if err != nil {
					clear(rootEnvelope)
					return err
				}
				ordered, err := searchIndexOrderKey(tree.kind, rootNode.postings[len(rootNode.postings)-1].fields, id)
				if err != nil {
					clear(rootEnvelope)
					return err
				}
				copy(children[i].upper[:], ordered)
				clear(ordered)
				children[i].id = store.searchIndexTreeChildID(tree.kind, tree.digest, branchID, children[i].upper, searchIndexTreeLeaf, byte(i&1))
			}
			children[len(children)-1] = searchIndexTreeChild{
				upper: searchIndexTreeInfinity,
				id:    store.searchIndexTreeChildID(tree.kind, tree.digest, branchID, searchIndexTreeInfinity, searchIndexTreeLeaf, 1),
			}
			if err := tx.deleteKnownEntryLocked(store.searchIndexTreeKey(tree.kind, tree.digest, tree.root), rootEnvelope); err != nil {
				clear(rootEnvelope)
				return err
			}
			if err := tx.putSearchIndexTreeNodeLocked(tree.kind, tree.digest, leafID, searchIndexTreeNode{leaf: true, postings: rootNode.postings}, nil); err != nil {
				clear(rootEnvelope)
				return err
			}
			if err := tx.putSearchIndexTreeNodeLocked(tree.kind, tree.digest, branchID, searchIndexTreeNode{children: children}, nil); err != nil {
				clear(rootEnvelope)
				return err
			}
			rootChildren := []searchIndexTreeChild{{upper: searchIndexTreeInfinity, id: branchID}}
			if err := tx.putSearchIndexTreeNodeLocked(tree.kind, tree.digest, tree.root, searchIndexTreeNode{children: rootChildren}, nil); err != nil {
				clear(rootEnvelope)
				return err
			}
			clear(rootEnvelope)
		}
		return nil
	})
}

func incrementHexID(sourceID string, amount int) (string, error) {
	decoded, err := hex.DecodeString(sourceID)
	if err != nil || len(decoded) != sha256.Size {
		clear(decoded)
		return "", ErrCorrupt
	}
	for step := 0; step < amount; step++ {
		for index := len(decoded) - 1; index >= 0; index-- {
			decoded[index]++
			if decoded[index] != 0 {
				break
			}
			if index == 0 {
				clear(decoded)
				return "", ErrCorrupt
			}
		}
	}
	result := hex.EncodeToString(decoded)
	clear(decoded)
	return result, nil
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
	if err := store.DeleteObservationRecord(context.Background(), first.ContentID()); err != nil {
		t.Fatal(err)
	}
	response, err := search.New(store).Search(context.Background(), search.Request{Query: "alpha beta", ResultLimit: 10, SnippetBytes: 64, ResponseBytes: 4096, ScanLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].SourceID != second.ContentID() {
		t.Fatalf("delete left a stale posting or removed a sibling: %+v", response.Results)
	}
	if err := store.DeleteObservationRecord(context.Background(), first.ContentID()); err != nil {
		t.Fatalf("repeated deletion: %v", err)
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

func TestDurableIndexMaxTermRebuildFitsBoundedBatch(t *testing.T) {
	path := t.TempDir() + "/max-term-rebuild.db"
	key := durableTestKey(t, 0x94)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	common := make([]string, search.MaxIndexedObservationTerms-2)
	for i := range common {
		common[i] = fmt.Sprintf("maxrebuild%03d", i)
	}
	want := indexObservation(t, "editor", "docs", strings.Join(common, " "))
	terms, err := observationSearchTerms(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(terms) != search.MaxIndexedObservationTerms || len(terms)+2 != 130 {
		t.Fatalf("max observation has %d term trees plus app/domain, want 130", len(terms)+2)
	}
	cost, err := estimatedSearchIndexOperations(want)
	if err != nil {
		t.Fatal(err)
	}
	if cost != 930 || cost+2 > bolt.MaxTransactionOps || bolt.MaxTransactionOps-(cost+2) < 64 {
		t.Fatalf("max-term rebuild batch estimate=%d plus fixed batch work=2, want <=%d with >=64-op headroom", cost, bolt.MaxTransactionOps)
	}
	if err := store.PutObservation(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	legacyMarker, err := store.seal(searchIndexStateKey, "search-index-state-v1", []byte{searchIndexFormatVersion - 1, 1})
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
		t.Fatalf("write v3-ready marker: %v", err)
	}
	clear(legacyMarker)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatalf("rebuild a max-term observation in the bounded batch: %v", err)
	}
	defer store.Close()
	response, err := search.New(store).Search(context.Background(), search.Request{
		Query: common[0], ResultLimit: 2, SnippetBytes: 64,
		ResponseBytes: 4096, ScanLimit: 1,
	})
	if err != nil || len(response.Results) != 1 || response.Results[0].SourceID != want.ContentID() || response.Truncated {
		t.Fatalf("search after max-term bounded rebuild results=%+v truncated=%t err=%v", response.Results, response.Truncated, err)
	}
	t.Logf("max-term rebuild: term_trees=%d app_domain_trees=2 estimate=%d batch_fixed_ops=2 declared_ops=%d hard_cap=%d", len(terms), cost, cost+2, bolt.MaxTransactionOps)
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

func TestDecodeSearchIndexTreeRejectsZeroPendingSiblingPivot(t *testing.T) {
	var childID [sha256.Size]byte
	childID[0] = 1
	encoded, err := encodeSearchIndexTreeNode(searchIndexTreeNode{
		children: []searchIndexTreeChild{{upper: searchIndexTreeInfinity, id: childID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A nonzero right-sibling ID with a zero pivot is not a canonical B-link.
	encoded[searchIndexTreeHeaderBytes] = 1
	if _, err := decodeSearchIndexTreeNode(encoded, searchIndexTerm); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("decode zero-pivot sibling link error=%v, want ErrCorrupt", err)
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
