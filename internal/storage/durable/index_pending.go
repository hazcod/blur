package durable

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const (
	searchIndexPending        byte = 0x01
	searchIndexProof          byte = 0x21
	searchIndexSourcePage     byte = 0x22
	searchPagedReverseVersion byte = 5
	// This limits work per transaction, never the number of terms in a page.
	exactIndexBatchTerms = 64
)

type pagedSearchProgress struct{ Indexed, Total, ErasedPages uint32 }

func encodePagedSearchReverse(progress pagedSearchProgress, sourceBytes int, refs []searchIndexRef) ([]byte, error) {
	if progress.Indexed > progress.Total || progress.Total > capture.MaxObservationTextBytes+128+253 || progress.ErasedPages > (progress.Indexed+exactIndexBatchTerms-1)/exactIndexBatchTerms || progress.Indexed < progress.Total && progress.Indexed%exactIndexBatchTerms != 0 || len(refs) > 2 {
		return nil, ErrCorrupt
	}
	for _, ref := range refs {
		if ref.kind == searchIndexTerm || ref.page != 0 || ref.fields != 0 {
			return nil, ErrCorrupt
		}
	}
	tail, err := encodeSearchIndexReverse(sourceBytes, refs)
	if err != nil {
		return nil, err
	}
	encoded := make([]byte, 13+len(tail))
	encoded[0] = searchPagedReverseVersion
	binary.BigEndian.PutUint32(encoded[1:5], progress.Indexed)
	binary.BigEndian.PutUint32(encoded[5:9], progress.Total)
	binary.BigEndian.PutUint32(encoded[9:13], progress.ErasedPages)
	copy(encoded[13:], tail)
	clear(tail)
	return encoded, nil
}

func decodePagedSearchReverse(encoded []byte) (pagedSearchProgress, int, []searchIndexRef, error) {
	if len(encoded) < 20 || encoded[0] != searchPagedReverseVersion || encoded[13] != searchIndexStructureVersion {
		return pagedSearchProgress{}, 0, nil, ErrCorrupt
	}
	progress := pagedSearchProgress{binary.BigEndian.Uint32(encoded[1:5]), binary.BigEndian.Uint32(encoded[5:9]), binary.BigEndian.Uint32(encoded[9:13])}
	sourceBytes, refs, err := decodeSearchIndexReverse(encoded[13:])
	if err != nil {
		return progress, 0, nil, err
	}
	if progress.Indexed > progress.Total || progress.Total > capture.MaxObservationTextBytes+128+253 || progress.ErasedPages > (progress.Indexed+exactIndexBatchTerms-1)/exactIndexBatchTerms || progress.Indexed < progress.Total && progress.Indexed%exactIndexBatchTerms != 0 || len(refs) > 2 {
		return progress, 0, nil, ErrCorrupt
	}
	for _, ref := range refs {
		if ref.kind == searchIndexTerm || ref.page != 0 || ref.fields != 0 {
			return progress, 0, nil, ErrCorrupt
		}
	}
	return progress, sourceBytes, refs, nil
}

func (s *Store) pendingSearchKey(sourceID string) []byte {
	return s.logicalKey(s.exactGroup(), searchIndexPending, s.exactNamespace("search-pending-v1"), sourceID)
}
func (s *Store) searchProofKey(sourceID string, digest [sha256.Size]byte) []byte {
	return s.logicalKey(s.exactGroup(), searchIndexProof, s.exactNamespace("search-proof-v1"), sourceID+hex.EncodeToString(digest[:]))
}
func (s *Store) searchSourcePageKey(sourceID string, page uint32) []byte {
	return s.logicalKey(s.exactGroup(), searchIndexSourcePage, s.exactNamespace("search-source-page-v1"), fmt.Sprintf("%s:%08x", sourceID, page))
}

// Raw evidence and this ID-only work receipt commit together. No posting-tree
// operation or term cardinality can reject the primary observation.
func (tx *searchIndexTx) queueObservationExactLocked(observation capture.SanitizedObservation) error {
	root, err := encodePagedSearchReverse(pagedSearchProgress{}, observationSearchBytes(observation), nil)
	if err != nil {
		return err
	}
	key := tx.store.searchReverseKey(observation.ContentID())
	err = tx.putSearchIndexValueLocked(key, "search-index-reverse-v4", root, false)
	clear(key)
	clear(root)
	if err != nil {
		return err
	}
	key = tx.store.pendingSearchKey(observation.ContentID())
	id, _ := hex.DecodeString(observation.ContentID())
	err = tx.putSearchIndexValueLocked(key, "search-pending-v1", id, false)
	clear(key)
	clear(id)
	if err == nil {
		if _, smallErr := observationSearchTermsLimit(observation, search.InlineObservationTerms); smallErr == nil {
			tx.exactInline = append(tx.exactInline, observation)
		}
	}
	return err
}

func (s *Store) indexSmallObservation(ctx context.Context, observation capture.SanitizedObservation) error {
	return s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		if err := tx.checkLocked(); err != nil {
			return tx.failLocked(err)
		}
		pendingKey := s.pendingSearchKey(observation.ContentID())
		defer clear(pendingKey)
		receipt, pending, err := tx.searchIndexPlainLocked(pendingKey, "search-pending-v1")
		clear(receipt)
		if err != nil || !pending {
			return err
		}
		if _, live, err := s.getObservationReader(tx.writer, observation.ContentID()); err != nil || !live {
			if err != nil {
				return err
			}
			return ErrCorrupt
		}
		rootKey := s.searchReverseKey(observation.ContentID())
		defer clear(rootKey)
		plain, found, err := tx.searchIndexPlainLocked(rootKey, "search-index-reverse-v4")
		defer clear(plain)
		if err != nil {
			return err
		}
		if !found {
			return ErrCorrupt
		}
		progress, _, _, err := decodePagedSearchReverse(plain)
		if err != nil {
			return err
		}
		if progress.Indexed != 0 || progress.Total != 0 {
			return nil
		} // a background batch already owns this receipt
		if err := tx.indexObservationLocked(observation); err != nil {
			return tx.failLocked(err)
		}
		return tx.deleteEntryLocked(pendingKey)
	})
}

func (s *Store) firstPendingSearchReader(reader bolt.Reader, maxBytes int) (string, int, bool, error) {
	if maxBytes < 1 {
		return "", 0, true, nil
	}
	entries, more, err := reader.Scan(s.exactPendingPrefix(), nil, 1, maxBytes)
	if err != nil {
		return "", 0, false, mapStorePhysicalError(err)
	}
	defer clearBoltEntries(entries)
	if len(entries) == 0 {
		return "", 0, more, nil
	}
	entry := entries[0]
	used := len(entry.Key) + len(entry.Value)
	plain, err := s.open(entry.Key, s.exactNamespace("search-pending-v1"), entry.Value)
	defer clear(plain)
	if err != nil || len(plain) != sha256.Size {
		return "", used, false, ErrCorrupt
	}
	id := hex.EncodeToString(plain)
	expected := s.pendingSearchKey(id)
	defer clear(expected)
	if !bytes.Equal(expected, entry.Key) {
		return "", used, false, ErrCorrupt
	}
	return id, used, false, nil
}

// ExactIndexPending is content-free coverage information for user-facing search.
func (s *Store) ExactIndexPending(ctx context.Context) (bool, error) {
	var pending bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		id, _, blocked, err := s.firstPendingSearchReader(reader, 512)
		if blocked {
			return ErrCorrupt
		}
		pending = id != ""
		return err
	})
	return pending, err
}

// IndexPendingExact advances at most maxBatches bounded transactions. It needs
// no model, is safe alongside ingestion/erasure, and resumes committed work
// after restart. The return value reports whether work remains.
func (s *Store) IndexPendingExact(ctx context.Context, maxBatches int) (bool, error) {
	if ctx == nil || maxBatches < 1 || maxBatches > 1024 {
		return false, search.ErrInvalidRequest
	}
	var cachedID, cachedApp, cachedDomain string
	var cachedBytes int
	var terms map[string]byte
	var ordered []string
	for batch := 0; batch < maxBatches; batch++ {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		var id string
		var observation capture.SanitizedObservation
		err := s.view(ctx, func(reader bolt.Reader) error {
			var err error
			var blocked bool
			id, _, blocked, err = s.firstPendingSearchReader(reader, 512)
			if err != nil {
				return err
			}
			if blocked {
				return ErrCorrupt
			}
			if id == "" {
				return nil
			}
			if id == cachedID {
				return nil
			}
			var live bool
			observation, live, err = s.getObservationReader(reader, id)
			if err == nil && !live {
				return ErrCorrupt
			}
			return err
		})
		if err != nil {
			return true, err
		}
		if id == "" {
			return false, nil
		}
		if id != cachedID {
			terms, err = observationSearchTerms(observation)
			if err != nil {
				return true, err
			}
			ordered = sortedTermKeys(terms)
			cachedID, cachedApp, cachedDomain = id, observation.AppID(), observation.Domain()
			cachedBytes = observationSearchBytes(observation)
		}
		err = s.Update(ctx, func(tx *Tx) error {
			tx.mu.Lock()
			defer tx.mu.Unlock()
			if err := tx.checkLocked(); err != nil {
				return tx.failLocked(err)
			}
			pendingKey := s.pendingSearchKey(id)
			defer clear(pendingKey)
			receipt, pending, err := tx.searchIndexPlainLocked(pendingKey, "search-pending-v1")
			clear(receipt)
			if err != nil || !pending {
				return err
			} // another worker or erasure finished it
			primaryKey := s.logicalKey(groupRecord, kindObservation, "", id)
			live, err := tx.writer.Has(primaryKey)
			clear(primaryKey)
			if err != nil {
				return mapStorePhysicalError(err)
			}
			if !live {
				return ErrCorrupt
			}

			done, err := tx.observationSearch().indexSearchTermsBatchLocked(id, cachedApp, cachedDomain, cachedBytes, terms, ordered)
			if err != nil {
				return tx.failLocked(err)
			}
			if done {
				return tx.deleteEntryLocked(pendingKey)
			}
			return nil
		})
		if err != nil {
			return true, err
		}
	}
	return s.ExactIndexPending(ctx)
}

// removePagedSearchBatchLocked drains one reverse page during staged erasure.
// The final app/domain/root removal stays atomic with the primary source.
func (tx *searchIndexTx) removePagedSearchBatchLocked(sourceID string, drain bool) (bool, error) {
	rootKey := tx.store.searchReverseKey(sourceID)
	defer clear(rootKey)
	root, found, err := tx.searchIndexPlainLocked(rootKey, "search-index-reverse-v4")
	defer clear(root)
	if err != nil {
		return false, err
	}
	if !found {
		return false, ErrCorrupt
	}
	if len(root) == 0 {
		return false, ErrCorrupt
	}
	if root[0] != searchPagedReverseVersion {
		return true, nil
	}
	progress, sourceBytes, refs, err := decodePagedSearchReverse(root)
	if err != nil {
		return false, err
	}
	pages := (progress.Indexed + exactIndexBatchTerms - 1) / exactIndexBatchTerms
	if progress.ErasedPages < pages {
		if !drain {
			return false, nil
		}
		pageKey := tx.store.searchSourcePageKey(sourceID, progress.ErasedPages)
		defer clear(pageKey)
		page, found, err := tx.searchIndexPlainLocked(pageKey, "search-source-page-v1")
		defer clear(page)
		if err != nil {
			return false, err
		}
		if !found {
			return false, ErrCorrupt
		}
		_, pageRefs, err := decodeSearchIndexReverse(page)
		if err != nil {
			return false, err
		}
		if len(pageRefs) != min(exactIndexBatchTerms, int(progress.Indexed-progress.ErasedPages*exactIndexBatchTerms)) {
			return false, ErrCorrupt
		}
		for _, ref := range pageRefs {
			if ref.kind != searchIndexTerm || ref.page != 0 {
				return false, ErrCorrupt
			}
			if err := tx.removeSearchIndexTreePostingLocked(ref.kind, ref.digest, sourceID, ref.fields); err != nil {
				return false, err
			}
			key := tx.store.searchProofKey(sourceID, ref.digest)
			err := tx.deleteEntryLocked(key)
			clear(key)
			if err != nil {
				return false, err
			}
		}
		if err := tx.deleteEntryLocked(pageKey); err != nil {
			return false, err
		}
		progress.ErasedPages++
		encoded, err := encodePagedSearchReverse(progress, sourceBytes, refs)
		if err != nil {
			return false, err
		}
		err = tx.putSearchIndexValueLocked(rootKey, "search-index-reverse-v4", encoded, true)
		clear(encoded)
		return false, err
	}
	key := tx.store.pendingSearchKey(sourceID)
	defer clear(key)
	receipt, pending, err := tx.searchIndexPlainLocked(key, "search-pending-v1")
	clear(receipt)
	if err != nil {
		return false, err
	}
	if pending {
		if err := tx.deleteEntryLocked(key); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *Store) readSearchTermProof(reader bolt.Reader, sourceID string, digest [sha256.Size]byte, maxBytes int) (searchIndexRef, bool, int, bool, error) {
	key := s.searchProofKey(sourceID, digest)
	defer clear(key)
	encoded, found, used, blocked, err := readBoundedIndexValue(reader, key, maxBytes)
	defer clear(encoded)
	if err != nil || blocked || !found {
		return searchIndexRef{}, found, used, blocked, err
	}
	plain, err := s.open(key, s.exactNamespace("search-proof-v1"), encoded)
	defer clear(plain)
	if err != nil || len(plain) != 2 || plain[0] != 1 || plain[1] == 0 || plain[1]&^searchFieldMask != 0 {
		return searchIndexRef{}, false, used, false, ErrCorrupt
	}
	return searchIndexRef{kind: searchIndexTerm, digest: digest, fields: plain[1]}, true, used, false, nil
}
