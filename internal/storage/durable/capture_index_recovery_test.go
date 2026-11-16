package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// Construct capture payloads/metadata authenticated at schema6, without calling
// current publication (which must produce schema7 indexes). Only test data is
// written. The shared migration fixture provides the historical observation.
func writeLegacySixCaptures(t *testing.T, path string, key securestore.Key, texts []string, at time.Time) ([]capture.CaptureSummary, map[string][]byte) {
	t.Helper()
	ctx := context.Background()
	writeSchemaVersionFixture(t, path, key, 6)
	state, err := newStoreState(key, false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := bolt.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	state.db = db
	s := &Store{state: state}
	defer func(close func() error) { _ = close() }(s.Close)
	if err := s.readIdentityWithSchemaLimit(ctx, 6); err != nil {
		t.Fatal(err)
	}
	if err := s.capturePartIndex().clearSearchIndex(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.captureUpdate(ctx, func(tx *Tx) error {
		k := s.logicalKey(groupRecord, kindIndex, "index:"+captureNS, captureIndexBackfillKey)
		found, err := tx.writer.Has(k)
		if err != nil || !found {
			return err
		}
		return tx.deleteEntryLocked(k)
	}); err != nil {
		t.Fatal(err)
	}
	var summaries []capture.CaptureSummary
	payloads := map[string][]byte{}
	for n, text := range texts {
		h, parts, summary := makeStoredCapture(t, text, at.Add(time.Duration(n)*time.Hour))
		summaries = append(summaries, summary)
		encodedSummary, err := capture.MarshalAuthenticatedCaptureSummary(summary)
		if err != nil {
			t.Fatal(err)
		}
		edition := captureEditionRecord{Version: 1, Summary: encodedSummary, Owner: h.CaptureID(), Publication: fmt.Sprintf("%064x", n+100), LastSeen: h.CapturedAt().UnixNano(), Visits: 1}
		if err := s.captureUpdate(ctx, func(tx *Tx) error {
			root := h.InitialRoot()
			for _, part := range parts {
				after, err := capture.AdvanceCaptureRoot(root, part)
				if err != nil {
					return err
				}
				encoded, err := capture.MarshalAuthenticatedCapturePart(part)
				if err != nil {
					return err
				}
				if err := tx.capturePutJSONLocked(capturePartKey(h.CaptureID(), part.Index()), captureStoredPart{Version: 1, Before: root, After: after, Part: encoded}, captureStoredPartBytes); err != nil {
					return err
				}
				root = after
			}
			if err := tx.captureLinkEditionLocked(summary.ContentID(), h.FamilyID(), &edition); err != nil {
				return err
			}
			if err := tx.capturePutJSONLocked("edition:"+summary.ContentID(), edition, captureMetaBytes); err != nil {
				return err
			}
			if err := tx.capturePutJSONLocked(captureVisitKey(summary.ContentID(), 0), captureVisitRecord{Version: 1, Rows: []captureVisitRow{{CaptureID: h.CaptureID(), At: h.CapturedAt().UnixNano()}}}, captureMetaBytes); err != nil {
				return err
			}
			return tx.capturePutJSONLocked("receipt:"+h.CaptureID(), captureReceipt{Version: 1, EditionID: summary.ContentID(), FamilyID: h.FamilyID(), At: h.CapturedAt().UnixNano(), Index: 0}, captureMetaBytes)
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.view(ctx, func(reader bolt.Reader) error {
			for _, part := range parts {
				k := s.logicalKey(groupRecord, kindIndex, "index:"+captureNS, capturePartKey(h.CaptureID(), part.Index()))
				value, found, err := reader.Get(k)
				if err != nil || !found {
					return ErrCorrupt
				}
				payloads[string(k)] = value
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return summaries, payloads
}

func TestPublishedCaptureIndexMigratesLegacySixAndAdvancesDeletedBackfillCursor(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xd3)
	legacy, payloads := writeLegacySixCaptures(t, path, key, []string{"legacyfirst evidence", "legacysecond evidence"}, time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openDurableWithHook(path, key, OpenExisting, func() error { return context.Canceled }); !errors.Is(err, ErrMigration) {
		t.Fatal("schema6 migration did not fail atomically", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed schema7 migration rewrote historical data", err)
	}
	s, err := OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	if pending, err := s.CaptureIndexPending(ctx); err != nil || !pending {
		t.Fatal("legacy capture backfill was omitted", pending, err)
	}
	if err := s.DeleteObservationRecord(ctx, legacy[0].ContentID()); err != nil {
		t.Fatal("backfill target could not be deleted", err)
	}
	fresh := publishIndexedFixture(t, s, "freshcapture evidence", time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC))
	drainCaptureIndex(t, s)
	for _, term := range []string{"legacysecond", "freshcapture"} {
		page, err := s.capturePartIndex().SearchExact(ctx, scopedQuery(term))
		if err != nil || len(page.Hits) != 1 {
			t.Fatal("backfill blocked remaining or new captures", term, page, err)
		}
	}
	page, err := s.capturePartIndex().SearchExact(ctx, scopedQuery("legacyfirst"))
	if err != nil || len(page.Hits) != 0 {
		t.Fatal("deleted backfill target resurfaced", page, err)
	}
	// The surviving legacy payload is immutable despite new descriptor/work rows.
	header := legacy[1].Header()
	if err := s.view(ctx, func(reader bolt.Reader) error {
		k := s.logicalKey(groupRecord, kindIndex, "index:"+captureNS, capturePartKey(header.CaptureID(), 0))
		value, found, err := reader.Get(k)
		defer clear(value)
		if err != nil || !found || !bytes.Equal(value, payloads[string(k)]) {
			return ErrCorrupt
		}
		return nil
	}); err != nil {
		t.Fatal("backfill rewrote historical capture bytes", err)
	}
	if _, found, err := s.GetCapture(ctx, fresh.ContentID()); err != nil || !found {
		t.Fatal("new capture was lost", err)
	}
}

func TestPublishedCaptureIndexPartialProofErasureResumesBeforeServing(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xd4)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var text strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&text, "token%03d ", i)
	}
	summary := publishIndexedFixture(t, s, text.String(), time.Now().UTC())
	if more, err := s.IndexPendingCaptures(ctx, 1); err != nil || !more {
		t.Fatal("fixture was not partly indexed", more, err)
	}
	page, err := s.capturePartIndex().SearchExact(ctx, scopedQuery("token000"))
	if err != nil || len(page.Hits) != 1 {
		t.Fatal("first term batch was not indexed", page, err)
	}
	sourceID := page.Hits[0].SourceID
	err = s.deleteSourceStaged(ctx, summary.ContentID(), func() error {
		j, found, err := s.readSourceErasureLocked(ctx)
		if err != nil || !found {
			return ErrCorrupt
		}
		if j.Phase != 8 || j.Part != 0 {
			return nil
		}
		progressed := false
		err = s.captureErasureViewLocked(ctx, func(reader bolt.Reader) error {
			index := s.capturePartIndex()
			k := index.searchReverseKey(sourceID)
			encoded, found, err := reader.Get(k)
			if err != nil || !found {
				return err
			}
			plain, err := index.open(k, index.exactNamespace("search-index-reverse-v4"), encoded)
			if err != nil {
				return err
			}
			progress, _, _, err := decodePagedSearchReverse(plain)
			progressed = progress.ErasedPages > 0
			return err
		})
		if err != nil {
			return err
		}
		if progressed {
			return context.Canceled
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("did not interrupt a committed proof-page drain", err)
	}
	if _, err := s.CaptureIndexPending(ctx); !errors.Is(err, ErrDeletionPending) {
		t.Fatal("partial erasure admitted a normal reader", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal("partial indexed capture erasure did not recover", err)
	}
	if pending, err := s.CaptureIndexPending(ctx); err != nil || pending {
		t.Fatal("erased capture retained work", pending, err)
	}
	page, err = s.capturePartIndex().SearchExact(ctx, scopedQuery("token000"))
	if err != nil || len(page.Hits) != 0 {
		t.Fatal("partial index returned after erasure recovery", page, err)
	}
	if _, found, err := s.readIndexValue(ctx, captureNS, "index-part:"+sourceID, 512); err != nil || found {
		t.Fatal("part map survived recovered erasure", found, err)
	}
}

func TestPublishedCaptureIndexVisitRowErasureResumesWithinSplitTimeTree(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xd5)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	var summary capture.CaptureSummary
	for i := 0; i < 70; i++ {
		summary = publishIndexedFixture(t, s, "shared revisit evidence", at.Add(time.Duration(i)*time.Second))
	}
	drainCaptureIndex(t, s)
	err = s.deleteSourceStaged(ctx, summary.ContentID(), func() error {
		j, found, err := s.readSourceErasureLocked(ctx)
		if err != nil || !found {
			return ErrCorrupt
		}
		if j.Phase == 9 && j.VisitRow > 0 {
			return context.Canceled
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("no bounded persisted visit-row cursor", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal("visit-row erasure failed reopen", err)
	}
	if _, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil || found {
		t.Fatal("visit erasure retained edition", found, err)
	}
	if pending, err := s.CaptureIndexPending(ctx); err != nil || pending {
		t.Fatal("visit erasure retained work", pending, err)
	}
	digest := s.capturePartIndex().searchLookupDigest(captureIndexVisit, summary.ContentID())
	if err := s.view(ctx, func(reader bolt.Reader) error {
		it := newSearchIndexTreeIterator(ctx, reader, s.capturePartIndex(), captureIndexVisit, digest)
		var budgetPage search.ExactPage
		_, found, blocked, err := it.next(&budgetPage, search.MaxIndexBytes)
		if err != nil || found || blocked {
			return ErrCorrupt
		}
		return nil
	}); err != nil {
		t.Fatal("erased visits retained a time posting", err)
	}
}
