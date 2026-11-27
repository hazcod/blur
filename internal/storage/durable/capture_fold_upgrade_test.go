package durable

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// A store that last indexed at format 6 (accent folding only) holds "søren" and
// "strauß" as stored tokens. Opening must not scan, the background worker must
// rebuild them, and afterwards the plain spellings must find both an ordinary
// observation and a multi-part capture without touching sources.
func TestFoldTableUpgradeFindsObservationAndMultiPartCapture(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0x69)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	plain := indexObservation(t, "terminal.app", "", "retained observation: Søren Krag reviewed Strauß contract")
	if err := s.PutObservation(ctx, plain); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("Retained large page navigation and evidence. ", 1000) + "Søren Krag reviewed Strauß contract tail."
	summary := publishIndexedFixture(t, s, text, time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC))
	drainCaptureIndex(t, s)
	if summary.PartCount() < 2 {
		t.Fatal("fixture is not multi-part", summary.PartCount())
	}
	for {
		more, err := s.IndexPendingExact(ctx, 32)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	for _, term := range []string{"soren", "strauss"} {
		if page, err := s.SearchExact(ctx, scopedQuery(term)); err != nil || len(page.Hits) != 2 {
			t.Fatal("fixture did not index folded spelling", term, page, err)
		}
	}
	payloads := previousCaptureTokensAt(t, s, summary, false, 6, map[string]string{"soren": "søren", "strauss": "strauß"})
	// Observation index marker as the release with capture format 6 wrote it.
	marker, err := s.seal(s.exactStateKey(), s.exactNamespace("search-index-state-v1"), []byte{5, 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return (&searchIndexTx{Tx: tx, store: s}).writeEntryLocked(s.exactStateKey(), marker, true)
	}); err != nil {
		t.Fatal(err)
	}
	clear(marker)
	partIDs, _, err := s.ListCaptureSourceIDs(ctx, summary.ContentID(), -1, 64)
	if err != nil || len(partIDs) != int(summary.PartCount()) {
		t.Fatal("fixture part identities missing", partIDs, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal("opening at the previous content format failed", err)
	}
	if pending, err := s.CaptureIndexPending(ctx); err != nil || !pending {
		t.Fatal("format-6 capture tokens were not queued", pending, err)
	}
	if page, err := s.capturePartIndex().SearchExact(ctx, scopedQuery("soren")); err != nil || len(page.Hits) != 0 {
		t.Fatal("fixture did not reproduce the stored-token miss", page, err)
	}
	for round := 0; ; round++ {
		more, err := s.IndexPendingExact(ctx, 32)
		if err != nil || round > 1000 {
			t.Fatal("observation rebuild did not finish", err)
		}
		if !more {
			break
		}
	}
	for round := 0; ; round++ {
		more, err := s.IndexPendingCaptures(ctx, 1)
		if err != nil || round > 200 {
			t.Fatal("capture migration did not finish", err)
		}
		if !more {
			break
		}
	}
	for _, term := range []string{"soren", "strauss"} {
		page, err := s.SearchExact(ctx, scopedQuery(term))
		if err != nil || len(page.Hits) != 2 || page.IndexingPending {
			t.Fatal("upgraded store does not find the folded spelling", term, page, err)
		}
		sawObservation, sawCapture := false, false
		for _, hit := range page.Hits {
			sawObservation = sawObservation || hit.SourceID == plain.ContentID()
			for _, id := range partIDs {
				sawCapture = sawCapture || hit.SourceID == id
			}
		}
		if !sawObservation || !sawCapture {
			t.Fatal("hits missed the observation or the capture part", term, page)
		}
	}
	if err := s.view(ctx, func(reader bolt.Reader) error {
		for k, before := range payloads {
			after, found, err := reader.Get([]byte(k))
			if err != nil || !found || !bytes.Equal(before, after) {
				return ErrCorrupt
			}
		}
		return nil
	}); err != nil {
		t.Fatal("migration rewrote source ciphertext", err)
	}
	for _, id := range partIDs {
		if resolved, found, err := s.ResolveCaptureSource(ctx, id); err != nil || !found || resolved != summary.ContentID() {
			t.Fatal("migration changed source identity", id, resolved, found, err)
		}
	}
	before := scopeCiphertext(t, s, capturePartSearchGroup)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := s.CaptureIndexPending(ctx); err != nil || pending || !equalCiphertext(before, scopeCiphertext(t, s, capturePartSearchGroup)) {
		t.Fatal("current-format store reworked on reopen", pending, err)
	}
	if more, err := s.IndexPendingExact(ctx, 32); err != nil || more {
		t.Fatal("current-format observation index reworked on reopen", more, err)
	}
}
