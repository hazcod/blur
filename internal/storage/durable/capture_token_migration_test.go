package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// Replace only derived rows with authenticated format-4 proofs. In particular,
// the old accented term really differs from today's folded token; changing a
// marker on already-current postings would not reproduce the recall defect.
func previousCaptureTokens(t *testing.T, s *Store, summary capture.CaptureSummary, partial bool) map[string][]byte {
	t.Helper()
	return previousCaptureTokensAt(t, s, summary, partial, 0, map[string]string{"jose": "josé"})
}

// previousCaptureTokensAt installs proofs as a release at the given content
// format wrote them: spelled maps today's tokens to that release's spelling.
// Format 0 reproduces the oldest unversioned shape; any other format stamps
// the edition, part mappings, backfill cursor and index marker with it.
func previousCaptureTokensAt(t *testing.T, s *Store, summary capture.CaptureSummary, partial bool, format int, spelled map[string]string) map[string][]byte {
	t.Helper()
	ctx := context.Background()
	payloads := map[string][]byte{}
	var edition captureEditionRecord
	var parts []capture.CapturePart
	if err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		edition, _, _, err = s.captureEditionReader(reader, summary.ContentID())
		if err != nil {
			return err
		}
		for i := int64(0); i < summary.PartCount(); i++ {
			part, err := s.captureMemberPartReader(reader, edition, summary, i)
			if err != nil {
				return err
			}
			parts = append(parts, part)
			key := s.logicalKey(groupRecord, kindIndex, "index:"+captureNS, capturePartKey(edition.Owner, i))
			value, found, err := reader.Get(key)
			if err != nil || !found {
				return ErrCorrupt
			}
			payloads[string(key)] = value
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, part := range parts {
		for {
			done := false
			if err := s.captureUpdate(ctx, func(tx *Tx) error {
				var err error
				done, err = tx.eraseCapturePartIndexLocked(summary.ContentID(), edition, part.Index())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if done {
				break
			}
		}
		terms, err := searchFieldTermsLimit(part.Text(), summary.Header().AppID(), summary.Header().Domain(), "", capture.MaxCapturePartTextBytes+128+253)
		if err != nil {
			t.Fatal(err)
		}
		for current, old := range spelled {
			if field, found := terms[current]; found {
				delete(terms, current)
				terms[old] = field
			}
		}
		id := capturePartSourceID(summary.ContentID(), edition.Publication, part.Index())
		sourceBytes := len(part.Text()) + len(summary.Header().AppID()) + len(summary.Header().Domain())
		if err := s.captureUpdate(ctx, func(tx *Tx) error {
			scoped := tx.capturePartSearch()
			encoded, err := encodePagedSearchReverse(pagedSearchProgress{}, sourceBytes, nil)
			if err != nil {
				return err
			}
			if err := scoped.putSearchIndexValueLocked(scoped.store.searchReverseKey(id), "search-index-reverse-v4", encoded, false); err != nil {
				return err
			}
			if format != 0 {
				mapping := captureIndexPart{Version: captureIndexVersion, EditionID: summary.ContentID(), Publication: edition.Publication, Index: part.Index(), Bytes: len(part.Text()), Format: format}
				return tx.capturePutJSONLocked("index-part:"+id, mapping, 768)
			}
			legacy := struct {
				Version                int
				EditionID, Publication string
				Index                  int64
				Bytes                  int
			}{captureIndexVersion, summary.ContentID(), edition.Publication, part.Index(), len(part.Text())}
			return tx.capturePutJSONLocked("index-part:"+id, legacy, 512)
		}); err != nil {
			t.Fatal(err)
		}
		for {
			done := false
			if err := s.captureUpdate(ctx, func(tx *Tx) error {
				var err error
				done, err = tx.capturePartSearch().indexSearchTermsBatchLocked(id, summary.Header().AppID(), summary.Header().Domain(), sourceBytes, terms, sortedTermKeys(terms))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if done || partial && part.Index() == summary.PartCount()-1 {
				break
			}
		}
	}
	if err := s.captureUpdate(ctx, func(tx *Tx) error {
		if partial {
			edition.IndexPart--
			edition.IndexQueued = true
			if err := tx.capturePutJSONLocked(captureIndexPendingKey, captureCatalog{Version: 1, Head: summary.ContentID(), Tail: summary.ContentID(), Count: 1}, captureMetaBytes); err != nil {
				return err
			}
		}
		if format != 0 {
			edition.IndexFormat = format
		}
		encoded, err := json.Marshal(edition)
		if err != nil {
			return err
		}
		if format == 0 {
			// Preserve canonical struct field order and exact integer bytes.
			encoded = bytes.ReplaceAll(encoded, []byte(fmt.Sprintf(",\"IndexFormat\":%d", captureIndexContentFormat)), nil)
		}
		if err := tx.putIndexValueLocked(captureNS, "edition:"+summary.ContentID(), encoded); err != nil {
			return err
		}
		legacyBackfill := struct {
			Version int
			Next    string
			Format  int `json:",omitempty"`
		}{captureIndexVersion, "", format}
		if err := tx.capturePutJSONLocked(captureIndexBackfillKey, legacyBackfill, 256); err != nil {
			return err
		}
		// The oldest shape predates content formats; its marker was the
		// structure version of that release.
		marker := byte(4)
		if format != 0 {
			marker = byte(format)
		}
		return tx.capturePartSearch().putSearchIndexValueLocked(tx.store.capturePartIndex().exactStateKey(), "search-index-state-v1", []byte{marker, 1}, true)
	}); err != nil {
		t.Fatal(err)
	}
	return payloads
}

func captureTokenMigrationText() string {
	var text strings.Builder
	text.WriteString(strings.Repeat("Retained large page navigation and evidence. ", 1000))
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&text, "zdetail%03d ", i)
	}
	text.WriteString("José confirmed the tail correction.")
	return text.String()
}

func TestCaptureTokenUpgradeResumesFullAndPartialPartsWithoutRewritingSources(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint("partial=", partial), func(t *testing.T) {
			ctx := context.Background()
			path, key := durableTestPath(t), durableTestKey(t, 0x67)
			s, err := OpenDurable(path, key, CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			summary := publishIndexedFixture(t, s, captureTokenMigrationText(), time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC))
			drainCaptureIndex(t, s)
			payloads := previousCaptureTokens(t, s, summary, partial)
			partIDs, _, err := s.ListCaptureSourceIDs(ctx, summary.ContentID(), -1, 64)
			if err != nil || len(partIDs) != int(summary.PartCount()) {
				t.Fatal("fixture part identities missing", partIDs, err)
			}
			if page, err := s.capturePartIndex().SearchExact(ctx, scopedQuery("jose")); err != nil || len(page.Hits) != 0 {
				t.Fatal("fixture did not reproduce the old-token miss", page, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenDurable(path, key, OpenExisting)
			if err != nil {
				t.Fatal(err)
			}
			if pending, err := s.CaptureIndexPending(ctx); err != nil || !pending {
				t.Fatal("old capture tokens were not queued for background migration", pending, err)
			}
			if more, err := s.IndexPendingCaptures(ctx, 1); err != nil || !more {
				t.Fatal("one quantum claimed the entire large capture", more, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenDurable(path, key, OpenExisting)
			if err != nil {
				t.Fatal("interrupted token migration did not reopen", err)
			}
			for round := 0; ; round++ {
				more, err := s.IndexPendingCaptures(ctx, 1)
				if err != nil || round > 100 {
					t.Fatal("bounded migration did not finish", more, err)
				}
				// A cited source must remain inspectable and deletable between
				// every pair of transactions, including the final proof erasure.
				for _, id := range partIDs {
					if _, found, err := s.GetSourceEvidence(ctx, id); err != nil || !found {
						t.Fatal("migration hid retained source evidence", id, round, found, err)
					}
					if resolved, found, err := s.ResolveCaptureSource(ctx, id); err != nil || !found || resolved != summary.ContentID() {
						t.Fatal("migration lost a cited part's deletion identity", id, round, resolved, found, err)
					}
					at := summary.Header().CapturedAt()
					if sighting, found, err := s.SourceEvidenceSightingAt(ctx, id, at.Add(-time.Hour), at.Add(time.Hour)); err != nil || !found || !sighting.Equal(at) {
						t.Fatal("migration hid source visit provenance", id, round, sighting, found, err)
					}
				}
				if !more {
					break
				}
			}
			for _, term := range []string{"jose", "zdetail399"} {
				page, err := s.SearchExact(ctx, scopedQuery(term))
				if err != nil || len(page.Hits) != 1 || page.IndexingPending {
					t.Fatal("retokenized tail not searchable", term, page, err)
				}
				if err := s.view(ctx, func(reader bolt.Reader) error {
					edition, _, _, err := s.captureEditionReader(reader, summary.ContentID())
					if err != nil || page.Hits[0].SourceID != capturePartSourceID(summary.ContentID(), edition.Publication, summary.PartCount()-1) {
						return ErrCorrupt
					}
					index := s.capturePartIndex()
					oldTerm := index.searchLookupDigest(searchIndexTerm, "josé")
					found, err := reader.Has(index.searchProofKey(page.Hits[0].SourceID, oldTerm))
					if err != nil || found {
						return ErrCorrupt
					}
					return nil
				}); err != nil {
					t.Fatal("migration changed provenance or retained old proofs", err)
				}
			}
			visits, err := s.ListCaptureVisits(ctx, summary.ContentID(), "", 64)
			if err != nil || len(visits.Visits) != 1 || !visits.Visits[0].CapturedAt.Equal(summary.Header().CapturedAt()) {
				t.Fatal("content migration changed original visit times", visits, err)
			}
			if err := s.view(ctx, func(reader bolt.Reader) error {
				for key, before := range payloads {
					after, found, err := reader.Get([]byte(key))
					if err != nil || !found || !bytes.Equal(before, after) {
						return ErrCorrupt
					}
				}
				return nil
			}); err != nil {
				t.Fatal("token migration rewrote original source ciphertext", err)
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
				t.Fatal("completed token migration restarted on open", pending, err)
			}
		})
	}
}

func TestCaptureTokenUpgradeDeletionAndLegacyQueueHeadDoNotBlockNewCaptures(t *testing.T) {
	for _, beforeWork := range []bool{true, false} {
		t.Run(fmt.Sprint("beforeWork=", beforeWork), func(t *testing.T) {
			ctx := context.Background()
			path, key := durableTestPath(t), durableTestKey(t, 0x68)
			s, err := OpenDurable(path, key, CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
			first := publishIndexedFixture(t, s, "José saved firstcapture evidence.", at)
			second := publishIndexedFixture(t, s, captureTokenMigrationText(), at.Add(time.Hour))
			drainCaptureIndex(t, s)
			previousCaptureTokens(t, s, first, false)
			previousCaptureTokens(t, s, second, true)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenDurable(path, key, OpenExisting)
			if err != nil {
				t.Fatal(err)
			}
			deleted := first
			if !beforeWork {
				// The old queue head is the second edition, while backfill first
				// visits the first. Normalize the queued head independently.
				if _, err := s.IndexPendingCaptures(ctx, 3); err != nil {
					t.Fatal(err)
				}
				deleted = second
			}
			deleteID := deleted.ContentID()
			if !beforeWork {
				ids, _, err := s.ListCaptureSourceIDs(ctx, deleteID, -1, 1)
				if err != nil || len(ids) != 1 {
					t.Fatal("part identity missing during migration", ids, err)
				}
				deleteID = ids[0]
			}
			if err := s.DeleteObservationRecord(ctx, deleteID); err != nil {
				t.Fatal("deletion during old-token migration failed", err)
			}
			fresh := publishIndexedFixture(t, s, "José saved freshcapture evidence.", at.Add(24*time.Hour))
			drainCaptureIndex(t, s)
			page, err := s.SearchExact(ctx, scopedQuery("jose"))
			if err != nil || len(page.Hits) != 2 || page.IndexingPending {
				t.Fatal("migration blocked surviving or new captures", page, err)
			}
			if _, found, err := s.GetCapture(ctx, deleted.ContentID()); err != nil || found {
				t.Fatal("migration restored an erased capture", found, err)
			}
			before := partIndexCiphertext(t, s)
			publishIndexedFixture(t, s, "José saved freshcapture evidence.", at.Add(25*time.Hour))
			if pending, err := s.CaptureIndexPending(ctx); err != nil || pending || !equalCiphertext(before, partIndexCiphertext(t, s)) {
				t.Fatal("new visit requeued current content", pending, err)
			}
			if _, found, err := s.GetCapture(ctx, fresh.ContentID()); err != nil || !found {
				t.Fatal("new capture disappeared", found, err)
			}
		})
	}
}
