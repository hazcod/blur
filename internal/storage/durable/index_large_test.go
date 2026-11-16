package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
)

func largeIndexObservation(t *testing.T, words int) capture.SanitizedObservation {
	t.Helper()
	var text strings.Builder
	for n := 0; n < words; n++ {
		fmt.Fprintf(&text, "word%06d ", n)
	}
	text.WriteString("zzztailneedle")
	observation, err := capture.SanitizeObservation("browser.app", "docs.example", text.String(), capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

func TestLargePagePreservesCompleteSourceBeforeIndexing(t *testing.T) {
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xa7), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o := largeIndexObservation(t, 20000)
	if err := s.PutObservation(context.Background(), o); err != nil {
		t.Fatalf("valid large page was rejected by derived indexing: %v", err)
	}
	saved, found, err := s.GetObservation(context.Background(), o.ContentID())
	if err != nil || !found || saved.Text() != o.Text() {
		t.Fatal("complete original page was not retained", found, err)
	}
}

func largePageQuery(terms ...string) search.ExactQuery {
	return search.ExactQuery{Terms: terms, AppID: "browser.app", Domain: "docs.example", TopK: 10, MaxRows: 200, MaxBytes: search.MaxIndexBytes}
}

func TestLargePageIndexResumesAndRetrievesEndOfPage(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xa8)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	o := largeIndexObservation(t, 20000)
	if err := s.PutObservation(ctx, o); err != nil {
		t.Fatal(err)
	}
	page, err := s.SearchExact(ctx, largePageQuery("zzztailneedle"))
	if err != nil || len(page.Hits) != 0 || !page.IndexingPending {
		t.Fatal("pending coverage was hidden", page, err)
	}
	if pending, err := s.IndexPendingExact(ctx, 1); err != nil || !pending {
		t.Fatal("first batch did not retain work", pending, err)
	}
	page, err = s.SearchExact(ctx, largePageQuery("word000000"))
	if err != nil || len(page.Hits) != 1 || !page.IndexingPending {
		t.Fatal("partial index not usable/disclosed", page, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{o.ContentID(), "zzztailneedle", "word019999", "browser.app", "docs.example"} {
		if bytes.Contains(disk, []byte(private)) {
			t.Fatal("index exposed plaintext", private)
		}
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := s.ExactIndexPending(ctx); err != nil || !pending {
		t.Fatal("restart lost queue", pending, err)
	}
	if pending, err := s.IndexPendingExact(ctx, 1024); err != nil || pending {
		t.Fatal("large page failed to finish", pending, err)
	}
	page, err = s.SearchExact(ctx, largePageQuery("word019999", "zzztailneedle"))
	if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != o.ContentID() || page.Hits[0].Score != 2 || page.IndexingPending {
		t.Fatal("tail terms not fully indexed", page, err)
	}
	q := largePageQuery("zzztailneedle")
	q.AppID = "other.app"
	page, err = s.SearchExact(ctx, q)
	if err != nil || len(page.Hits) != 0 {
		t.Fatal("app filter ignored", page, err)
	}
	// Bounded query proof reads must depend on query terms, not page vocabulary.
	if page, err = s.SearchExact(ctx, largePageQuery("zzztailneedle")); err != nil || page.Bytes > 32<<10 {
		t.Fatal("query scanned whole document index", page, err)
	}
	if pending, err := s.IndexPendingExact(ctx, 1); err != nil || pending {
		t.Fatal("completed work requeued", pending, err)
	}
}

func TestLargePageErasureResumesAndRemovesIndexReceipts(t *testing.T) {
	for _, finish := range []bool{false, true} {
		t.Run(fmt.Sprint(finish), func(t *testing.T) {
			ctx := context.Background()
			path, key := durableTestPath(t), durableTestKey(t, 0xa9)
			s, err := OpenDurable(path, key, CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			o := largeIndexObservation(t, 1024)
			if err := s.PutObservation(ctx, o); err != nil {
				t.Fatal(err)
			}
			batches := 3
			if finish {
				batches = 100
			}
			if _, err := s.IndexPendingExact(ctx, batches); err != nil {
				t.Fatal(err)
			}
			stopped := false
			err = s.deleteSourceStaged(ctx, o.ContentID(), func() error {
				journal, _, err := s.readSourceErasureLocked(ctx)
				if err != nil {
					return err
				}
				if journal.Phase == 7 {
					var progress pagedSearchProgress
					err := s.state.db.View(ctx, func(reader bolt.Reader) error {
						key := s.searchReverseKey(o.ContentID())
						defer clear(key)
						encoded, found, err := reader.Get(key)
						defer clear(encoded)
						if err != nil {
							return err
						}
						if !found {
							return ErrCorrupt
						}
						plain, err := s.open(key, "search-index-reverse-v4", encoded)
						defer clear(plain)
						if err != nil {
							return err
						}
						progress, _, _, err = decodePagedSearchReverse(plain)
						return err
					})
					if err != nil {
						return err
					}
					if progress.ErasedPages > 0 {
						stopped = true
						return context.Canceled
					}
				}
				return nil
			})
			if !stopped || !errors.Is(err, context.Canceled) {
				t.Fatal("erasure interruption failed", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenDurable(path, key, OpenExisting)
			if err != nil {
				t.Fatal(err)
			}
			if _, found, err := s.GetObservation(ctx, o.ContentID()); err != nil || found {
				t.Fatal("source survived recovery", found, err)
			}
			if more, err := s.IndexPendingExact(ctx, 100); err != nil || more {
				t.Fatal("deleted source requeued", more, err)
			}
			page, err := s.SearchExact(ctx, largePageQuery("word000000"))
			if err != nil || len(page.Hits) > 0 || page.IndexingPending {
				t.Fatal("erased postings survived", page, err)
			}
			err = s.view(ctx, func(reader bolt.Reader) error {
				for _, kind := range []byte{searchIndexPending, searchIndexProof, searchIndexSourcePage, searchIndexReverse} {
					entries, _, err := reader.Scan([]byte{searchIndexGroup, kind}, nil, 1, 8192)
					if err != nil {
						return err
					}
					if len(entries) != 0 {
						t.Fatalf("orphaned source index kind %x", kind)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLargePagePublicDeletionUsesBoundedCascade(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xaa), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o := largeIndexObservation(t, 2048)
	if err := s.PutObservationAt(ctx, o, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IndexPendingExact(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObservationRecord(ctx, o.ContentID()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetObservation(ctx, o.ContentID()); err != nil || found {
		t.Fatal(found, err)
	}
}

func TestDerivedIndexFailureNeverRollsBackAcceptedSource(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xab), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o := indexObservation(t, "browser.app", "docs.example", "zzztailneedle")
	// A deliberately malformed derived tree makes the post-commit inline
	// optimization fail. Raw source acceptance must remain independent.
	err = s.Update(ctx, func(tx *Tx) error {
		if err := tx.PutObservation(o); err != nil {
			return err
		}
		tx.mu.Lock()
		defer tx.mu.Unlock()
		digest := s.searchLookupDigest(searchIndexTerm, "zzztailneedle")
		key := s.searchIndexTreeKey(searchIndexTerm, digest, [32]byte{})
		defer clear(key)
		return tx.putSearchIndexValueLocked(key, "search-index-tree-v4", []byte{0xff}, false)
	})
	if err != nil {
		t.Fatal("derived failure rejected source", err)
	}
	if saved, live, err := s.GetObservation(ctx, o.ContentID()); err != nil || !live || saved.Text() != o.Text() {
		t.Fatal("raw source lost", live, err)
	}
	if pending, err := s.ExactIndexPending(ctx); err != nil || !pending {
		t.Fatal("failed indexing lost its work receipt", pending, err)
	}
	if _, err := s.IndexPendingExact(ctx, 1); !errors.Is(err, ErrCorrupt) {
		t.Fatal("derived corruption was not reported", err)
	}
	// Repair is reconstructed exclusively from the retained encrypted sources.
	if err := s.clearSearchIndex(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureSearchIndex(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := s.SearchExact(ctx, largePageQuery("zzztailneedle"))
	if err != nil || len(page.Hits) != 1 || page.IndexingPending {
		t.Fatal("saved source could not be reindexed", page, err)
	}
}

func TestMaximumSizePageKeepsTailSearchable(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xac), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	footer := largeIndexObservation(t, 256).Text()
	prefixBytes := capture.MaxObservationTextBytes - len(footer)
	text := strings.Repeat("padding ", prefixBytes/8) + strings.Repeat(" ", prefixBytes%8) + footer
	o, err := capture.SanitizeObservation("browser.app", "docs.example", text, capture.MaxObservationTextBytes)
	if err != nil || len(o.Text()) != capture.MaxObservationTextBytes {
		t.Fatal("invalid maximum-size fixture", len(o.Text()), err)
	}
	if err := s.PutObservation(ctx, o); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.IndexPendingExact(ctx, 10); err != nil || pending {
		t.Fatal("maximum-size page failed indexing", pending, err)
	}
	page, err := s.SearchExact(ctx, largePageQuery("zzztailneedle"))
	if err != nil || len(page.Hits) != 1 || page.IndexingPending {
		t.Fatal(page, err)
	}
	if saved, found, err := s.GetObservation(ctx, o.ContentID()); err != nil || !found || saved.Text() != text {
		t.Fatal("maximum-size source was truncated", found, err)
	}
}

func TestExactIndexResumesAfterProcessExitWithoutClose(t *testing.T) {
	ctx := context.Background()
	key := durableTestKey(t, 0xad)
	if os.Getenv("BLUR_EXACT_CRASH_HELPER") == "1" {
		s, err := OpenDurable(os.Getenv("BLUR_EXACT_CRASH_PATH"), key, OpenExisting)
		if err != nil {
			t.Fatal(err)
		}
		if more, err := s.IndexPendingExact(ctx, 3); err != nil || !more {
			t.Fatal(more, err)
		}
		os.Exit(0)
	}
	path := durableTestPath(t)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	o := largeIndexObservation(t, 2048)
	if err := s.PutObservation(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestExactIndexResumesAfterProcessExitWithoutClose$")
	command.Env = append(os.Environ(), "BLUR_EXACT_CRASH_HELPER=1", "BLUR_EXACT_CRASH_PATH="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("index crash helper: %v %s", err, output)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	if pending, err := s.ExactIndexPending(ctx); err != nil || !pending {
		t.Fatal("process exit lost work", pending, err)
	}
	if pending, err := s.IndexPendingExact(ctx, 100); err != nil || pending {
		t.Fatal(pending, err)
	}
	page, err := s.SearchExact(ctx, largePageQuery("zzztailneedle"))
	if err != nil || len(page.Hits) != 1 || page.IndexingPending {
		t.Fatal(page, err)
	}
}

func FuzzDecodePagedSearchReverse(f *testing.F) {
	encoded, err := encodePagedSearchReverse(pagedSearchProgress{}, 0, nil)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(encoded)
	f.Fuzz(func(t *testing.T, encoded []byte) {
		progress, sourceBytes, refs, err := decodePagedSearchReverse(encoded)
		if err != nil {
			return
		}
		canonical, err := encodePagedSearchReverse(progress, sourceBytes, refs)
		if err != nil || !bytes.Equal(encoded, canonical) {
			t.Fatal("invalid paged reverse round trip", err)
		}
	})
}
