package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func captureIndexForTest(t *testing.T, s *Store) *Store {
	t.Helper()
	provider, ok := any(s).(interface{ capturePartIndex() *Store })
	if !ok {
		t.Fatal("capture parts have no independent exact index")
	}
	return provider.capturePartIndex()
}

func initializeScopedIndex(t *testing.T, s *Store, group byte) {
	t.Helper()
	if err := s.Update(context.Background(), func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		found, err := tx.writer.Has([]byte{group, 0})
		if err != nil || found {
			return err
		}
		return tx.writeSearchIndexStateLocked()
	}); err != nil {
		t.Fatal(err)
	}
}

func addScopedObservation(s *Store, o capture.SanitizedObservation) error {
	return s.Update(context.Background(), func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return tx.indexObservationLocked(o)
	})
}

func scopedQuery(term string) search.ExactQuery {
	return search.ExactQuery{Terms: []string{term}, TopK: search.MaxResults, MaxRows: search.MaxIndexRows, MaxBytes: search.MaxIndexBytes}
}

func scopeCiphertext(t *testing.T, s *Store, group byte) map[string][]byte {
	t.Helper()
	rows := map[string][]byte{}

	var after []byte
	for round := 0; round < 64; round++ {
		more := false
		var next []byte
		if err := s.view(context.Background(), func(r bolt.Reader) error {
			entries, hasMore, err := r.Scan([]byte{group}, after, 256, 8<<20)
			defer clearBoltEntries(entries)
			if err != nil {
				return err
			}
			more = hasMore
			for _, e := range entries {
				rows[string(e.Key)] = bytes.Clone(e.Value)
			}
			if len(entries) > 0 {
				next = bytes.Clone(entries[len(entries)-1].Key)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !more {
			return rows
		}
		if len(next) == 0 || bytes.Compare(next, after) <= 0 {
			t.Fatal("fixture snapshot made no cursor progress")
		}
		after = next
	}
	t.Fatal("fixture exceeded snapshot page budget")
	return rows
}

func TestCaptureIndexScopeIsolatesQueriesClearAndReopen(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xc1)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	c := captureIndexForTest(t, s)
	initializeScopedIndex(t, c, 0x41)
	observation := indexObservation(t, "editor.app", "notes.example", "shared observationonly")
	part := indexObservation(t, "browser.app", "pages.example", "shared captureonly")
	if err := addScopedObservation(s, observation); err != nil {
		t.Fatal(err)
	}
	if err := addScopedObservation(c, part); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		index       *Store
		want        string
		app, domain string
	}{
		{s, observation.ContentID(), "editor.app", "notes.example"},
		{c, part.ContentID(), "browser.app", "pages.example"},
	} {
		q := scopedQuery("shared")
		q.AppID, q.Domain = tc.app, tc.domain
		page, err := tc.index.SearchExact(ctx, q)
		if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != tc.want {
			t.Fatal("scope query or filters crossed indexes", page, err)
		}
	}
	if s.searchLookupDigest(searchIndexTerm, "shared") == c.searchLookupDigest(searchIndexTerm, "shared") {
		t.Fatal("scope lookup keys expose equal term digests")
	}
	before := scopeCiphertext(t, s, 0x40)
	if err := c.clearSearchIndex(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, scopeCiphertext(t, s, 0x40)) {
		t.Fatal("capture clear changed observation ciphertext")
	}
	if len(scopeCiphertext(t, s, 0x41)) != 0 {
		t.Fatal("capture clear left capture index rows")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal("scoped writes broke authenticated reopen", err)
	}
	page, err := s.SearchExact(ctx, scopedQuery("observationonly"))
	if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != observation.ContentID() {
		t.Fatal("observation index changed on reopen", page, err)
	}
	cleared, err := captureIndexForTest(t, s).SearchExact(ctx, scopedQuery("captureonly"))
	if err != nil || len(cleared.Hits) != 0 {
		t.Fatal("cleared capture scope returned old rows after initialization", cleared, err)
	}
}

func TestCaptureIndexScopeKeepsSameSourceReverseAndPendingIndependent(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	c := captureIndexForTest(t, s)
	initializeScopedIndex(t, c, 0x41)
	id := strings.Repeat("a", 64)
	for _, tc := range []struct {
		index *Store
		term  string
	}{{s, "observationword"}, {c, "captureword"}} {
		if err := tc.index.Update(ctx, func(tx *Tx) error {
			tx.mu.Lock()
			defer tx.mu.Unlock()
			page, digest, err := tx.addSearchIndexPostingLocked(searchIndexTerm, tc.term, id, 1)
			if err != nil {
				return err
			}
			encoded, err := encodeSearchIndexReverse(24, []searchIndexRef{{kind: searchIndexTerm, digest: digest, page: page, fields: 1}})
			if err != nil {
				return err
			}
			return tx.putSearchIndexValueLocked(tx.store.searchReverseKey(id), "search-index-reverse-v4", encoded, true)
		}); err != nil {
			t.Fatal(err)
		}
		page, err := tc.index.SearchExact(ctx, scopedQuery(tc.term))
		if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != id {
			t.Fatal("same source ID lost its scoped reverse proof", page, err)
		}
	}
	if err := c.Update(ctx, func(tx *Tx) error { tx.mu.Lock(); defer tx.mu.Unlock(); return tx.removeSearchIndexSourceLocked(id) }); err != nil {
		t.Fatal(err)
	}
	page, err := s.SearchExact(ctx, scopedQuery("observationword"))
	if err != nil || len(page.Hits) != 1 {
		t.Fatal("capture removal erased the observation posting", page, err)
	}
	o := indexObservation(t, "editor.app", "notes.example", "pending identity in both scopes")
	for _, index := range []*Store{s, c} {
		if err := index.Update(ctx, func(tx *Tx) error { tx.mu.Lock(); defer tx.mu.Unlock(); return tx.queueObservationExactLocked(o) }); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.clearSearchIndex(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ExactIndexPending(ctx)
	if err != nil || !pending {
		t.Fatal("capture cleanup removed observation pending work", pending, err)
	}
	pending, err = c.ExactIndexPending(ctx)
	if err != nil || pending {
		t.Fatal("capture pending scan read observation work", pending, err)
	}
}

func TestCaptureIndexScopeRejectsCrossScopeCiphertextAndSharesGates(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	c := captureIndexForTest(t, s)
	initializeScopedIndex(t, c, 0x41)
	if err := s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		encoded, found, err := tx.writer.Get([]byte{0x40, 0})
		defer clear(encoded)
		if err != nil || !found {
			return ErrCorrupt
		}
		return tx.writeEntryLocked([]byte{0x41, 0}, encoded, true)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SearchExact(ctx, scopedQuery("shared")); !errors.Is(err, ErrCorrupt) {
		t.Fatal("capture scope accepted foreign authenticated state", err)
	}
	if _, err := s.SearchExact(ctx, scopedQuery("shared")); err != nil {
		t.Fatal("foreign state corrupted original scope", err)
	}
	s.state.deletionPending.Store(true)
	for _, index := range []*Store{s, c} {
		if _, err := index.SearchExact(ctx, scopedQuery("shared")); !errors.Is(err, ErrDeletionPending) {
			t.Fatal("scope bypassed deletion read gate", err)
		}
		if err := index.Update(ctx, func(*Tx) error { return nil }); !errors.Is(err, ErrDeletionPending) {
			t.Fatal("scope bypassed deletion write gate", err)
		}
	}
	s.state.deletionPending.Store(false)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(ctx, func(*Tx) error { return nil }); err == nil {
		t.Fatal("scope survived original database close")
	}
}

func TestCaptureIndexScopeConcurrentSplitsKeepOneValidManifest(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xc2)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	c := captureIndexForTest(t, s)
	initializeScopedIndex(t, c, 0x41)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for n, index := range []*Store{s, c} {
		// Prepare fixtures outside goroutines so failures remain on the test thread.
		sources := make([]capture.SanitizedObservation, 132)
		for i := range sources {
			sources[i] = indexObservation(t, fmt.Sprintf("scope%d.app", n), "scope.example", fmt.Sprintf("shared item%03d scope%d", i, n))
		}
		wg.Add(1)
		go func(index *Store, sources []capture.SanitizedObservation) {
			defer wg.Done()
			for _, o := range sources {
				if err := addScopedObservation(index, o); err != nil {
					results <- err
					return
				}
			}
			results <- nil
		}(index, sources)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	for n, index := range []*Store{s, c} {
		q := scopedQuery("shared")
		q.AppID = fmt.Sprintf("scope%d.app", n)
		page, err := index.SearchExact(ctx, q)
		if err != nil || len(page.Hits) != search.MaxResults || !page.HasMore {
			t.Fatal("split tree lost scoped ranking", len(page.Hits), page.HasMore, err)
		}
		q.MaxBytes = 1
		if _, err := index.SearchExact(ctx, q); !errors.Is(err, search.ErrIndexBudget) {
			t.Fatal("scoped query ignored byte budget", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal("concurrent scope manifest failed verification", err)
	}
	for _, index := range []*Store{s, captureIndexForTest(t, s)} {
		page, err := index.SearchExact(ctx, scopedQuery("shared"))
		if err != nil || len(page.Hits) != search.MaxResults {
			t.Fatal("split scope failed after reopen", page, err)
		}
	}
}

// A copied transaction/manifest, or a scope change left on Tx.store, loses or
// misroutes at least one of these writes even when each tree works separately.
func TestCaptureIndexScopeFacadeSharesAtomicTransaction(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	c := captureIndexForTest(t, s)
	initializeScopedIndex(t, c, 0x41)
	before := indexObservation(t, "editor.app", "notes.example", "before observation")
	part := indexObservation(t, "browser.app", "pages.example", "capturepayload")
	after := indexObservation(t, "editor.app", "notes.example", "after observation")
	write := func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		if err := tx.indexObservationLocked(before); err != nil {
			return err
		}
		if err := tx.capturePartSearch().indexObservationLocked(part); err != nil {
			return err
		}
		return tx.indexObservationLocked(after)
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		if err := write(tx); err != nil {
			return err
		}
		return context.Canceled
	}); !errors.Is(err, context.Canceled) {
		t.Fatal("fixture did not abort transaction", err)
	}
	for _, tc := range []struct {
		index *Store
		term  string
	}{{s, "before"}, {c, "capturepayload"}, {s, "after"}} {
		page, err := tc.index.SearchExact(ctx, scopedQuery(tc.term))
		if err != nil || len(page.Hits) != 0 {
			t.Fatal("aborted scoped mutation escaped transaction", page, err)
		}
	}
	if err := s.Update(ctx, write); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		index    *Store
		term, id string
	}{{s, "before", before.ContentID()}, {c, "capturepayload", part.ContentID()}, {s, "after", after.ContentID()}} {
		page, err := tc.index.SearchExact(ctx, scopedQuery(tc.term))
		if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != tc.id {
			t.Fatal("facade lost or misrouted original transaction mutation", page, err)
		}
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return tx.capturePartSearch().removeSearchIndexSourceLocked(part.ContentID())
	}); err != nil {
		t.Fatal(err)
	}
	page, err := c.SearchExact(ctx, scopedQuery("capturepayload"))
	if err != nil || len(page.Hits) != 0 {
		t.Fatal("original transaction could not erase scoped index", page, err)
	}
}

// The large observation is a test fixture for shared tree primitives, not a
// production capture loader. Capture-aware publication/work remains separate.
func TestCaptureIndexScopePagedProofsAndErasureStayIndependent(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	c := captureIndexForTest(t, s)
	initializeScopedIndex(t, c, 0x41)
	var text strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&text, "vocabulary%03d ", i)
	}
	o, err := capture.SanitizeObservation("editor.app", "notes.example", text.String(), 32<<10)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservation(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(ctx, func(tx *Tx) error { tx.mu.Lock(); defer tx.mu.Unlock(); return tx.queueObservationExactLocked(o) }); err != nil {
		t.Fatal(err)
	}
	for _, index := range []*Store{s, c} {
		pending := true
		for batch := 0; batch < 16 && pending; batch++ {
			pending, err = index.IndexPendingExact(ctx, 1)
			if err != nil {
				t.Fatal("scoped paged work could not resume", err)
			}
		}
		if pending {
			t.Fatal("bounded fixture did not finish indexing")
		}
		page, err := index.SearchExact(ctx, scopedQuery("vocabulary299"))
		if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != o.ContentID() {
			t.Fatal("scoped paged proof lost tail term", page, err)
		}
	}
	done := false
	for step := 0; step < 16 && !done; step++ {
		if err := s.Update(ctx, func(tx *Tx) error {
			tx.mu.Lock()
			defer tx.mu.Unlock()
			var err error
			done, err = tx.capturePartSearch().removePagedSearchBatchLocked(o.ContentID(), true)
			if err != nil || !done {
				return err
			}
			return tx.capturePartSearch().removeSearchIndexSourceLocked(o.ContentID())
		}); err != nil {
			t.Fatal("scoped reverse-page drain failed", err)
		}
	}
	if !done {
		t.Fatal("scoped reverse-page drain did not finish")
	}
	for _, tc := range []struct {
		index *Store
		want  int
	}{{s, 1}, {c, 0}} {
		page, err := tc.index.SearchExact(ctx, scopedQuery("vocabulary299"))
		if err != nil || len(page.Hits) != tc.want {
			t.Fatal("scoped paged erasure crossed indexes", page, err)
		}
	}
}

func TestCaptureIndexScopeRejectsForeignTreeCiphertext(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	c := captureIndexForTest(t, s)
	initializeScopedIndex(t, c, 0x41)
	o := indexObservation(t, "editor.app", "notes.example", "shared tree payload")
	for _, index := range []*Store{s, c} {
		if err := addScopedObservation(index, o); err != nil {
			t.Fatal(err)
		}
	}
	sourceKey := s.searchIndexTreeKey(searchIndexTerm, s.searchLookupDigest(searchIndexTerm, "shared"), [32]byte{})
	targetKey := c.searchIndexTreeKey(searchIndexTerm, c.searchLookupDigest(searchIndexTerm, "shared"), [32]byte{})
	if err := s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		encoded, found, err := tx.writer.Get(sourceKey)
		defer clear(encoded)
		if err != nil || !found {
			return ErrCorrupt
		}
		return tx.writeEntryLocked(targetKey, encoded, true)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SearchExact(ctx, scopedQuery("shared")); !errors.Is(err, ErrCorrupt) {
		t.Fatal("capture query accepted a foreign tree node", err)
	}
	page, err := s.SearchExact(ctx, scopedQuery("shared"))
	if err != nil || len(page.Hits) != 1 {
		t.Fatal("foreign-node rejection affected original tree", page, err)
	}
}
