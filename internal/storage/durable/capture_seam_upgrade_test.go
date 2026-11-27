package durable

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// Install actual format5 per-part token proofs, not merely an old marker on
// today's postings. Primary capture ciphertext is never rewritten by this.
func formatFiveCaptureFixture(t *testing.T, s *Store, summary capture.CaptureSummary) map[string][]byte {
	t.Helper()
	ctx := context.Background()
	payloads := map[string][]byte{}
	var edition captureEditionRecord
	if err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		edition, _, _, err = s.captureEditionReader(reader, summary.ContentID())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for index := int64(0); index < summary.PartCount(); index++ {
		var part capture.CapturePart
		if err := s.view(ctx, func(reader bolt.Reader) error {
			var err error
			part, err = s.captureMemberPartReader(reader, edition, summary, index)
			if err != nil {
				return err
			}
			key := s.logicalKey(groupRecord, kindIndex, "index:"+captureNS, capturePartKey(edition.Owner, index))
			value, found, err := reader.Get(key)
			if err != nil || !found {
				return ErrCorrupt
			}
			payloads[string(key)] = value
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		sourceID := capturePartSourceID(summary.ContentID(), edition.Publication, index)
		if index == 0 {
			for {
				done := false
				if err := s.captureUpdate(ctx, func(tx *Tx) error {
					var err error
					done, err = tx.eraseCapturePartIndexLocked(summary.ContentID(), edition, index)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if done {
					break
				}
			}
		}
		mapping := captureIndexPart{Version: 1, EditionID: summary.ContentID(), Publication: edition.Publication, Index: index, Bytes: len(part.Text()), Format: 5}
		terms, err := searchFieldTermsLimit(part.Text(), summary.Header().AppID(), summary.Header().Domain(), "", capture.MaxCapturePartTextBytes+128+253)
		if err != nil {
			t.Fatal(err)
		}
		ordered := sortedTermKeys(terms)
		if err := s.captureUpdate(ctx, func(tx *Tx) error {
			return tx.initializeCapturePartIndexLocked(sourceID, len(part.Text())+len(summary.Header().AppID())+len(summary.Header().Domain()), mapping)
		}); err != nil {
			t.Fatal(err)
		}
		for start := 0; start < len(ordered); start += exactIndexBatchTerms {
			end := min(len(ordered), start+exactIndexBatchTerms)
			if err := s.captureUpdate(ctx, func(tx *Tx) error {
				index := tx.capturePartSearch()
				key := index.store.searchReverseKey(sourceID)
				root, _, err := index.searchIndexPlainLocked(key, "search-index-reverse-v4")
				if err != nil {
					return err
				}
				progress, sourceBytes, refs, err := decodePagedSearchReverse(root)
				clear(root)
				if err != nil {
					return err
				}
				if start == 0 {
					for _, field := range []struct {
						kind  byte
						value string
					}{{searchIndexApp, summary.Header().AppID()}, {searchIndexDomain, summary.Header().Domain()}} {
						if field.value == "" {
							continue
						}
						_, digest, err := index.addSearchIndexPostingLocked(field.kind, field.value, sourceID, 0)
						if err != nil {
							return err
						}
						refs = append(refs, searchIndexRef{kind: field.kind, digest: digest})
					}
				}
				var page []searchIndexRef
				for _, term := range ordered[start:end] {
					mac := hmac.New(sha256.New, s.state.idKey)
					_, _ = io.WriteString(mac, index.store.exactNamespace("blur.durable.search.lookup.v1\x00"))
					_, _ = mac.Write([]byte{searchIndexTerm})
					writeMACString(mac, term)
					var digest [32]byte
					copy(digest[:], mac.Sum(nil))
					fields := terms[term]
					order, err := searchIndexOrderKey(searchIndexTerm, fields, sourceID)
					if err != nil {
						return err
					}
					if err := index.insertSearchIndexTreeLocked(searchIndexTerm, digest, order, searchIndexPosting{sourceID, fields}); err != nil {
						return err
					}
					if err := index.putSearchIndexValueLocked(index.store.searchProofKey(sourceID, digest), "search-proof-v1", []byte{1, fields}, false); err != nil {
						return err
					}
					page = append(page, searchIndexRef{kind: searchIndexTerm, digest: digest, fields: fields})
				}
				encoded, err := encodeSearchIndexReverse(0, page)
				if err != nil {
					return err
				}
				if err := index.putSearchIndexValueLocked(index.store.searchSourcePageKey(sourceID, uint32(start/exactIndexBatchTerms)), "search-source-page-v1", encoded, false); err != nil {
					return err
				}
				progress.Indexed = uint32(end)
				progress.Total = uint32(len(ordered))
				encoded, err = encodePagedSearchReverse(progress, sourceBytes, refs)
				if err != nil {
					return err
				}
				return index.putSearchIndexValueLocked(key, "search-index-reverse-v4", encoded, true)
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.captureUpdate(ctx, func(tx *Tx) error {
		if err := tx.unlinkCaptureIndexLocked(summary.ContentID(), &edition); err != nil {
			return err
		}
		edition.IndexFormat = 5
		edition.IndexPart = summary.PartCount()
		edition.IndexStream = nil
		edition.IndexRangeWork = nil
		if err := tx.capturePutJSONLocked("edition:"+summary.ContentID(), edition, captureMetaBytes); err != nil {
			return err
		}
		if err := tx.capturePutJSONLocked(captureIndexBackfillKey, captureIndexBackfill{Version: 1, Format: 5}, 256); err != nil {
			return err
		}
		return tx.capturePartSearch().putSearchIndexValueLocked(s.capturePartIndex().exactStateKey(), "search-index-state-v1", []byte{5, 1}, true)
	}); err != nil {
		t.Fatal(err)
	}
	return payloads
}

func TestCaptureSeamUpgradeRepairsAcceptedLargeSourceAcrossRestartWithoutPayloadMutation(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0x94)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	const word = "seamquartzproof"
	prefix := strings.Repeat("n ", (capture.MaxCapturePartTextBytes-7)/2)
	input := prefix + word + " " + strings.Repeat("retained continuation filler. ", 37000) + "savedtailproof"
	if len(input) <= 1<<20 {
		t.Fatal("large fixture too short")
	}
	summary := publishIndexedFixture(t, s, input, time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC))
	payloads := formatFiveCaptureFixture(t, s, summary)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := s.CaptureIndexPending(ctx); err != nil || !pending {
		t.Fatal("accepted old tokens were not queued", pending, err)
	}
	if _, err := s.IndexPendingCaptures(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	drainCaptureIndex(t, s)
	for _, term := range []string{word, "savedtailproof"} {
		page, err := s.SearchExact(ctx, scopedQuery(term))
		if err != nil || len(page.Hits) != 1 || page.IndexingPending {
			t.Fatal("upgrade lost accepted text", term, page, err)
		}
	}
	for _, fragment := range []string{"seamquar", "tzproof"} {
		page, err := s.SearchExact(ctx, scopedQuery(fragment))
		if err != nil || len(page.Hits) != 0 {
			t.Fatal("transport fragment remained a false exact word", fragment, page, err)
		}
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
		t.Fatal("upgrade changed original encrypted bytes", err)
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
		t.Fatal("completed upgrade restarted", pending, err)
	}
}
