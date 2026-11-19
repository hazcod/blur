package durable

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const (
	searchIndexGroup   byte = 0x40
	searchIndexState   byte = 0x00
	searchIndexTerm    byte = 0x10
	searchIndexApp     byte = 0x11
	searchIndexDomain  byte = 0x12
	searchIndexReverse byte = 0x7f

	// searchIndexFormatVersion is the version of what the observation index
	// contains, stamped in its ready marker. Version 5 adds the window-title
	// field (bit 0x08) and folds diacritics and possessives in
	// search.NormalizeTokens. A store whose marker is older is rebuilt by
	// ensureSearchIndex: the index is cleared and every retained observation
	// is queued through the pending-index backfill, which indexes it in the
	// background. Bump it whenever field bits or tokenization change.
	searchIndexFormatVersion byte = 5
	// searchIndexStructureVersion is the version byte of the page, tree and
	// reverse-link encodings. It is independent of the content version above
	// so a content-only bump never makes an existing record undecodable (the
	// capture-part index shares these encodings).
	searchIndexStructureVersion byte = 4

	searchIndexPageRows        = 64
	searchIndexPageHeader      = 2
	searchIndexRefBytes        = 1 + sha256.Size + 4 + 1
	searchIndexMaxRefs         = search.InlineObservationTerms + 2
	searchIndexReverseKeyBytes = 2 + sha256.Size
)

// Posting field bits: which fields of a source contain the term. A term found
// in several fields carries several bits.
const (
	searchFieldText   byte = 0x01
	searchFieldApp    byte = 0x02
	searchFieldDomain byte = 0x04
	searchFieldTitle  byte = 0x08
	searchFieldMask        = searchFieldText | searchFieldApp | searchFieldDomain | searchFieldTitle
)

var (
	searchIndexStateKey = []byte{searchIndexGroup, searchIndexState}
)

type searchIndexPosting struct {
	sourceID string
	fields   byte
}

type searchIndexRef struct {
	kind   byte
	digest [sha256.Size]byte
	page   uint32
	fields byte
}

type searchIndexConstraint struct {
	kind   byte
	digest [sha256.Size]byte
}

var _ search.ExactIndex = (*Store)(nil)

func (s *Store) searchLookupDigest(kind byte, value string) [sha256.Size]byte {
	mac := hmac.New(sha256.New, s.state.idKey)
	_, _ = io.WriteString(mac, s.exactNamespace("blur.durable.search.lookup.v1\x00"))
	_, _ = mac.Write([]byte{kind})
	writeMACString(mac, value)
	var digest [sha256.Size]byte
	sum := mac.Sum(nil)
	copy(digest[:], sum)
	clear(sum)
	return digest
}

func (s *Store) searchReverseDigest(sourceID string) [sha256.Size]byte {
	mac := hmac.New(sha256.New, s.state.idKey)
	_, _ = io.WriteString(mac, s.exactNamespace("blur.durable.search.reverse.v1\x00"))
	writeMACString(mac, sourceID)
	var digest [sha256.Size]byte
	sum := mac.Sum(nil)
	copy(digest[:], sum)
	clear(sum)
	return digest
}

func (s *Store) searchReverseKey(sourceID string) []byte {
	digest := s.searchReverseDigest(sourceID)
	key := make([]byte, searchIndexReverseKeyBytes)
	key[0], key[1] = s.exactGroup(), searchIndexReverse
	copy(key[2:], digest[:])
	clear(digest[:])
	return key
}

func encodeSearchIndexPage(postings []searchIndexPosting) ([]byte, error) {
	if len(postings) > searchIndexPageRows {
		return nil, ErrCorrupt
	}
	ordered := append([]searchIndexPosting(nil), postings...)
	sort.Slice(ordered, func(i, j int) bool { return searchIndexPostingLess(ordered[i], ordered[j]) })
	encoded := []byte{searchIndexStructureVersion, byte(len(ordered))}
	var previous []byte
	for i, posting := range ordered {
		if !validHash(posting.sourceID) || posting.fields&^searchFieldMask != 0 || i > 0 && !searchIndexPostingLess(ordered[i-1], posting) {
			return nil, ErrCorrupt
		}
		id, err := hex.DecodeString(posting.sourceID)
		if err != nil || len(id) != sha256.Size {
			clear(id)
			return nil, ErrCorrupt
		}
		prefix := 0
		for prefix < len(previous) && prefix < len(id) && previous[prefix] == id[prefix] {
			prefix++
		}
		encoded = append(encoded, byte(prefix))
		encoded = append(encoded, id[prefix:]...)
		encoded = append(encoded, posting.fields)
		clear(previous)
		previous = id
	}
	clear(previous)
	return encoded, nil
}

func decodeSearchIndexPage(encoded []byte) ([]searchIndexPosting, error) {
	maxEncoded := searchIndexPageHeader + searchIndexPageRows*(1+sha256.Size+1)
	if len(encoded) < searchIndexPageHeader || len(encoded) > maxEncoded || encoded[0] != searchIndexStructureVersion || int(encoded[1]) > searchIndexPageRows {
		return nil, ErrCorrupt
	}
	count := int(encoded[1])
	postings := make([]searchIndexPosting, 0, count)
	offset := searchIndexPageHeader
	var previous []byte
	for i := 0; i < count; i++ {
		if offset >= len(encoded) {
			clear(previous)
			return nil, ErrCorrupt
		}
		prefix := int(encoded[offset])
		offset++
		if prefix > sha256.Size || prefix > len(previous) || sha256.Size-prefix+1 > len(encoded)-offset {
			clear(previous)
			return nil, ErrCorrupt
		}
		id := make([]byte, sha256.Size)
		copy(id, previous[:prefix])
		suffixBytes := sha256.Size - prefix
		copy(id[prefix:], encoded[offset:offset+suffixBytes])
		offset += suffixBytes
		fields := encoded[offset]
		offset++
		if fields&^searchFieldMask != 0 {
			clear(id)
			clear(previous)
			return nil, ErrCorrupt
		}
		sourceID := hex.EncodeToString(id)
		posting := searchIndexPosting{sourceID: sourceID, fields: fields}
		if len(postings) > 0 && !searchIndexPostingLess(postings[len(postings)-1], posting) {
			clear(id)
			clear(previous)
			return nil, ErrCorrupt
		}
		postings = append(postings, posting)
		clear(previous)
		previous = id
	}
	clear(previous)
	if offset != len(encoded) {
		return nil, ErrCorrupt
	}
	return postings, nil
}

func encodeSearchIndexReverse(sourceBytes int, refs []searchIndexRef) ([]byte, error) {
	if sourceBytes < 0 || sourceBytes > capture.MaxObservationTextBytes+128+253 || len(refs) > searchIndexMaxRefs {
		return nil, ErrCorrupt
	}
	ordered := append([]searchIndexRef(nil), refs...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].kind != ordered[j].kind {
			return ordered[i].kind < ordered[j].kind
		}
		return bytes.Compare(ordered[i].digest[:], ordered[j].digest[:]) < 0
	})
	encoded := make([]byte, 7+len(ordered)*searchIndexRefBytes)
	encoded[0] = searchIndexStructureVersion
	binary.BigEndian.PutUint32(encoded[1:5], uint32(sourceBytes))
	binary.BigEndian.PutUint16(encoded[5:7], uint16(len(ordered)))
	position := 7
	for i, ref := range ordered {
		if !validSearchIndexKind(ref.kind) || ref.fields&^searchFieldMask != 0 || i > 0 && ordered[i-1].kind == ref.kind && ordered[i-1].digest == ref.digest {
			clear(encoded)
			return nil, ErrCorrupt
		}
		encoded[position] = ref.kind
		copy(encoded[position+1:position+1+sha256.Size], ref.digest[:])
		binary.BigEndian.PutUint32(encoded[position+1+sha256.Size:position+1+sha256.Size+4], ref.page)
		encoded[position+searchIndexRefBytes-1] = ref.fields
		position += searchIndexRefBytes
	}
	return encoded, nil
}

func decodeSearchIndexReverse(encoded []byte) (int, []searchIndexRef, error) {
	if len(encoded) > 0 && encoded[0] == searchPagedReverseVersion {
		_, sourceBytes, refs, err := decodePagedSearchReverse(encoded)
		return sourceBytes, refs, err
	}
	if len(encoded) < 7 || encoded[0] != searchIndexStructureVersion {
		return 0, nil, ErrCorrupt
	}
	sourceBytesValue := binary.BigEndian.Uint32(encoded[1:5])
	count := int(binary.BigEndian.Uint16(encoded[5:7]))
	if sourceBytesValue > uint32(capture.MaxObservationTextBytes+128+253) || count > searchIndexMaxRefs || len(encoded) != 7+count*searchIndexRefBytes {
		return 0, nil, ErrCorrupt
	}
	sourceBytes := int(sourceBytesValue)
	refs := make([]searchIndexRef, 0, count)
	position := 7
	for i := 0; i < count; i++ {
		ref := searchIndexRef{kind: encoded[position]}
		copy(ref.digest[:], encoded[position+1:position+1+sha256.Size])
		ref.page = binary.BigEndian.Uint32(encoded[position+1+sha256.Size : position+1+sha256.Size+4])
		ref.fields = encoded[position+searchIndexRefBytes-1]
		if !validSearchIndexKind(ref.kind) || ref.fields&^searchFieldMask != 0 || i > 0 && !searchIndexRefLess(refs[len(refs)-1], ref) {
			clear(ref.digest[:])
			return 0, nil, ErrCorrupt
		}
		refs = append(refs, ref)
		position += searchIndexRefBytes
	}
	return sourceBytes, refs, nil
}

func validSearchIndexKind(kind byte) bool {
	return kind == searchIndexTerm || kind == searchIndexApp || kind == searchIndexDomain
}

func searchIndexRefLess(a, b searchIndexRef) bool {
	if a.kind != b.kind {
		return a.kind < b.kind
	}
	return bytes.Compare(a.digest[:], b.digest[:]) < 0
}

func (tx *searchIndexTx) searchIndexPlainLocked(key []byte, namespace string) ([]byte, bool, error) {
	encoded, found, err := tx.writer.Get(key)
	if err != nil {
		return nil, false, mapStorePhysicalError(err)
	}
	if !found {
		return nil, false, nil
	}
	plain, err := tx.store.open(key, tx.store.exactNamespace(namespace), encoded)
	clear(encoded)
	if err != nil {
		return nil, false, ErrCorrupt
	}
	return plain, true, nil
}

func (tx *searchIndexTx) putSearchIndexValueLocked(key []byte, namespace string, plaintext []byte, replace bool) error {
	envelope, err := tx.store.seal(key, tx.store.exactNamespace(namespace), plaintext)
	if err != nil {
		return err
	}
	defer clear(envelope)
	return tx.writeEntryLocked(key, envelope, replace)
}

func (tx *searchIndexTx) addSearchIndexPostingLocked(kind byte, value, sourceID string, fields byte) (uint32, [sha256.Size]byte, error) {
	digest := tx.store.searchLookupDigest(kind, value)
	ordered, err := searchIndexOrderKey(kind, fields, sourceID)
	if err != nil {
		clear(digest[:])
		return 0, [sha256.Size]byte{}, err
	}
	err = tx.insertSearchIndexTreeLocked(kind, digest, ordered, searchIndexPosting{sourceID: sourceID, fields: fields})
	clear(ordered)
	if err != nil {
		clear(digest[:])
		return 0, [sha256.Size]byte{}, err
	}
	return 0, digest, nil
}

func observationSearchTerms(observation capture.SanitizedObservation) (map[string]byte, error) {
	return observationSearchTermsLimit(observation, capture.MaxObservationTextBytes+128+253)
}

func observationSearchTermsLimit(observation capture.SanitizedObservation, limit int) (map[string]byte, error) {
	return searchFieldTermsLimit(observation.Text(), observation.AppID(), observation.Domain(), observation.Title(), limit)
}
func searchFieldTermsLimit(text, app, domain, title string, limit int) (map[string]byte, error) {
	terms := make(map[string]byte)
	fields := []struct {
		value string
		mask  byte
	}{{text, searchFieldText}, {app, searchFieldApp}, {domain, searchFieldDomain}, {title, searchFieldTitle}}
	for _, field := range fields {
		normalized, err := search.NormalizeTokens(field.value, limit)
		if err != nil {
			return nil, &storage.StoreError{Code: storage.Invalid}
		}
		for _, term := range normalized {
			terms[term] |= field.mask
			if len(terms) > limit {
				return nil, &storage.StoreError{Code: storage.Invalid}
			}
		}
	}
	return terms, nil
}

// indexObservationLocked extends the exact index in the observation's writer
// transaction. The caller owns tx.mu and stores no searchable value in keys.
func (tx *searchIndexTx) indexObservationLocked(observation capture.SanitizedObservation) error {
	if observation.Validate() != nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	terms, err := observationSearchTermsLimit(observation, search.InlineObservationTerms)
	if err != nil {
		return err
	}
	refs := make([]searchIndexRef, 0, len(terms)+2)
	for _, term := range sortedTermKeys(terms) {
		page, digest, err := tx.addSearchIndexPostingLocked(searchIndexTerm, term, observation.ContentID(), terms[term])
		if err != nil {
			clear(digest[:])
			return err
		}
		refs = append(refs, searchIndexRef{kind: searchIndexTerm, digest: digest, page: page, fields: terms[term]})
		clear(digest[:])
	}
	if appID := observation.AppID(); appID != "" {
		page, digest, err := tx.addSearchIndexPostingLocked(searchIndexApp, appID, observation.ContentID(), 0)
		if err != nil {
			clear(digest[:])
			return err
		}
		refs = append(refs, searchIndexRef{kind: searchIndexApp, digest: digest, page: page})
		clear(digest[:])
	}
	if domain := observation.Domain(); domain != "" {
		page, digest, err := tx.addSearchIndexPostingLocked(searchIndexDomain, domain, observation.ContentID(), 0)
		if err != nil {
			clear(digest[:])
			return err
		}
		refs = append(refs, searchIndexRef{kind: searchIndexDomain, digest: digest, page: page})
		clear(digest[:])
	}
	reversePlain, err := encodeSearchIndexReverse(observationSearchBytes(observation), refs)
	if err != nil {
		return err
	}
	reverseKey := tx.store.searchReverseKey(observation.ContentID())
	err = tx.putSearchIndexValueLocked(reverseKey, "search-index-reverse-v4", reversePlain, true)
	clear(reversePlain)
	clear(reverseKey)
	return err
}

func sortedTermKeys(terms map[string]byte) []string {
	keys := make([]string, 0, len(terms))
	for term := range terms {
		keys = append(keys, term)
	}
	sort.Strings(keys)
	return keys
}

func estimatedSearchIndexOperations(observation capture.SanitizedObservation) (int, error) {
	terms, err := observationSearchTerms(observation)
	if err != nil {
		return 0, err
	}
	// A term or exact filter tree uses at most seven bounded operations,
	// including one leaf split and (when needed) publication/repair of one
	// deferred branch sibling. App and domain are separate posting trees.
	// Reserve another twenty for the observation, size row, reverse link, and
	// manifest work. The physical writer remains the authoritative hard cap.
	treeCount := len(terms)
	if observation.AppID() != "" {
		treeCount++
	}
	if observation.Domain() != "" {
		treeCount++
	}
	return treeCount*7 + 20, nil
}

func (tx *Tx) deleteEntryLocked(key []byte) error {
	value, found, err := tx.writer.Get(key)
	if err != nil {
		return mapStorePhysicalError(err)
	}
	if !found {
		return ErrCorrupt
	}
	if err := tx.manifest.removeKey(key, value, tx.store.state.manifestKey); err != nil {
		clear(value)
		return err
	}
	clear(value)
	if err := tx.writer.Delete(key); err != nil {
		return mapStorePhysicalError(err)
	}
	tx.dirty = true
	return nil
}

func (tx *Tx) deleteKnownEntryLocked(key, value []byte) error {
	if err := tx.manifest.removeKey(key, value, tx.store.state.manifestKey); err != nil {
		return err
	}
	if err := tx.writer.Delete(key); err != nil {
		return mapStorePhysicalError(err)
	}
	tx.dirty = true
	return nil
}

func (tx *searchIndexTx) removeSearchIndexSourceLocked(sourceID string) error {
	if done, err := tx.removePagedSearchBatchLocked(sourceID, false); err != nil || !done {
		if err != nil {
			return err
		}
		return ErrTransactionBudget // coordinated erasure will drain bounded pages
	}
	reverseKey := tx.store.searchReverseKey(sourceID)
	reversePlain, found, err := tx.searchIndexPlainLocked(reverseKey, "search-index-reverse-v4")
	if err != nil || !found {
		clear(reverseKey)
		clear(reversePlain)
		if err != nil {
			return err
		}
		return ErrCorrupt
	}
	clear(reverseKey)
	_, refs, err := decodeSearchIndexReverse(reversePlain)
	clear(reversePlain)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if ref.page != 0 {
			return ErrCorrupt
		}
		if err := tx.removeSearchIndexTreePostingLocked(ref.kind, ref.digest, sourceID, ref.fields); err != nil {
			return err
		}
	}

	reverseKey = tx.store.searchReverseKey(sourceID)
	if err := tx.deleteEntryLocked(reverseKey); err != nil {
		clear(reverseKey)
		return err
	}
	clear(reverseKey)
	return nil
}

// deleteObservation is the low-level source/index removal used only inside a
// coordinated transaction.
func (tx *Tx) deleteObservation(sourceID string) error {
	if tx == nil {
		return &storage.StoreError{Code: storage.Closed}
	}
	tx.mu.Lock()
	err := tx.removeNoteDocumentSourceLocked(sourceID)
	tx.mu.Unlock()
	if err != nil {
		return err
	}
	for step := 0; ; step++ {
		if step >= 8 {
			return ErrTransactionBudget
		}
		drained, err := tx.eraseObservationSighting(sourceID)
		if err != nil {
			return err
		}
		if drained {
			break
		}
	}
	// Remove this source from the exact-hour recovery index in the same
	// transaction, regardless of which deletion entry point was used.
	if err := tx.removeCaptureHourSource(sourceID); err != nil {
		return err
	}
	if err := tx.removeObservationTimelineSource(sourceID); err != nil {
		return err
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.checkLocked(); err != nil {
		return tx.failLocked(err)
	}
	if !validHash(sourceID) {
		return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
	}
	if _, found, err := tx.store.getObservationReader(tx.writer, sourceID); err != nil {
		return tx.failLocked(err)
	} else if !found {
		return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
	}
	if err := tx.removeSearchIndexSourceLocked(sourceID); err != nil {
		return tx.failLocked(err)
	}
	recordKey := tx.store.logicalKey(groupRecord, kindObservation, "", sourceID)
	orderKey := tx.store.logicalKey(groupOrder, kindObservation, "", sourceID)
	if err := tx.deleteEntryLocked(recordKey); err != nil {
		clear(recordKey)
		clear(orderKey)
		return tx.failLocked(err)
	}
	if err := tx.deleteEntryLocked(orderKey); err != nil {
		clear(recordKey)
		clear(orderKey)
		return tx.failLocked(err)
	}
	clear(recordKey)
	clear(orderKey)
	return nil
}

func (tx *Tx) deleteObservationRecord(sourceID string) error {
	if tx == nil {
		return &storage.StoreError{Code: storage.Closed}
	}
	tx.mu.Lock()
	if err := tx.checkLocked(); err != nil {
		defer tx.mu.Unlock()
		return tx.failLocked(err)
	}
	if !validHash(sourceID) {
		defer tx.mu.Unlock()
		return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
	}
	if _, found, err := tx.store.captureIndexPartReader(tx.writer, sourceID); err != nil || found {
		if err == nil {
			err = ErrDeletionPending
		}
		defer tx.mu.Unlock()
		return tx.failLocked(err)
	}
	if _, _, found, err := tx.store.captureEditionReader(tx.writer, sourceID); err != nil || found {
		if err == nil {
			err = ErrDeletionPending
		}
		defer tx.mu.Unlock()
		return tx.failLocked(err)
	}
	tx.mu.Unlock()
	if err := tx.requireProjectionIndexes(); err != nil {
		return err
	}
	tx.mu.Lock()
	if err := tx.checkLocked(); err != nil {
		tx.mu.Unlock()
		return tx.failLocked(err)
	}
	_, found, err := tx.store.getObservationReader(tx.writer, sourceID)
	if err != nil {
		defer tx.mu.Unlock()
		return tx.failLocked(err)
	}
	tx.mu.Unlock()
	if err := tx.removeTodaySource(sourceID); err != nil {
		return err
	}
	if err := tx.removeActivitySource(sourceID); err != nil {
		return err
	}
	if err := tx.removeChroniclesForSource(sourceID); err != nil {
		return err
	}
	if err := tx.removeSemanticSource(sourceID); err != nil {
		return err
	}
	if err := tx.removeQuestionHistorySource(sourceID); err != nil {
		return err
	}
	if err := tx.removeEnrichmentSource(sourceID); err != nil {
		return err
	}
	if err := tx.removeTasksSource(sourceID); err != nil {
		return err
	}
	if err := tx.quarantineKnowledgeSource(sourceID); err != nil {
		return err
	}
	tx.mu.Lock()
	meetingErr := tx.removeMeetingSourceLocked(sourceID)
	tx.mu.Unlock()
	if meetingErr != nil {
		return meetingErr
	}
	if !found {
		return nil
	}
	return tx.deleteObservation(sourceID)
}

func (tx *searchIndexTx) writeSearchIndexStateLocked() error {
	plain := []byte{searchIndexFormatVersion, 1}
	return tx.putSearchIndexValueLocked(tx.store.exactStateKey(), "search-index-state-v1", plain, false)
}

func (s *Store) ensureSearchIndex(ctx context.Context) error {
	ready := false
	err := s.view(ctx, func(reader bolt.Reader) error {
		envelope, found, err := reader.Get(s.exactStateKey())
		if err != nil {
			return mapStorePhysicalError(err)
		}
		if !found {
			return nil
		}
		plain, err := s.open(s.exactStateKey(), s.exactNamespace("search-index-state-v1"), envelope)
		clear(envelope)
		if err != nil || len(plain) != 2 || plain[0] == 0 || plain[0] > searchIndexFormatVersion || plain[1] != 1 {
			clear(plain)
			return ErrCorrupt
		}
		ready = plain[0] == searchIndexFormatVersion
		clear(plain)
		return nil
	})
	if err != nil || ready {
		return err
	}
	if err := s.clearSearchIndex(ctx); err != nil {
		return err
	}
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := s.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: storage.MaxObservationPageRows, MaxBytes: storage.MaxObservationPageBytes, After: after})
		if err != nil {
			return err
		}
		if len(page.Observations) == 0 {
			break
		}
		for start := 0; start < len(page.Observations); {
			end, operations := start, 2 // reserve the manifest load and flush
			for end < len(page.Observations) {
				cost := 20 // only durable queue/root writes belong to this transaction
				if end > start && operations+cost > bolt.MaxTransactionOps {
					break
				}
				if operations+cost > bolt.MaxTransactionOps {
					return ErrCorrupt
				}
				operations += cost
				end++
			}
			batch := page.Observations[start:end]
			if err := s.Update(ctx, func(tx *Tx) error {
				tx.mu.Lock()
				defer tx.mu.Unlock()
				if err := tx.checkLocked(); err != nil {
					return tx.failLocked(err)
				}
				for _, observation := range batch {
					if err := tx.queueObservationExactLocked(observation); err != nil {
						return tx.failLocked(err)
					}
				}
				// A rebuild only queues work receipts. The post-commit inline
				// indexing used for fresh captures would tokenize every
				// retained observation before open returns; leaving the
				// receipts pending hands that work to the background index
				// controller (IndexPendingExact), and searches report
				// IndexingPending until it finishes.
				tx.exactInline = nil
				return nil
			}); err != nil {
				return err
			}
			after = batch[len(batch)-1].ContentID()
			start = end
		}
	}
	return s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		if err := tx.checkLocked(); err != nil {
			return tx.failLocked(err)
		}
		return tx.writeSearchIndexStateLocked()
	})
}

func (s *Store) clearSearchIndex(ctx context.Context) error {
	var after []byte
	for {
		var next []byte
		var more bool
		err := s.Update(ctx, func(tx *Tx) error {
			tx.mu.Lock()
			defer tx.mu.Unlock()
			if err := tx.checkLocked(); err != nil {
				return tx.failLocked(err)
			}
			entries, hasMore, err := tx.writer.Scan(s.exactPrefix(), after, 256, 8<<20)
			if err != nil {
				return tx.failLocked(mapStorePhysicalError(err))
			}
			defer clearBoltEntries(entries)
			more = hasMore
			for _, entry := range entries {
				if err := tx.deleteKnownEntryLocked(entry.Key, entry.Value); err != nil {
					return tx.failLocked(err)
				}
			}
			if len(entries) > 0 {
				next = bytes.Clone(entries[len(entries)-1].Key)
			}
			return nil
		})
		if err != nil {
			clear(after)
			clear(next)
			return err
		}
		clear(after)
		after = next
		if more && len(after) == 0 {
			return ErrCorrupt
		}
		if !more {
			clear(after)
			return nil
		}
	}
}

func readBoundedIndexValue(reader bolt.Reader, key []byte, maxBytes int) ([]byte, bool, int, bool, error) {
	if maxBytes <= 0 {
		return nil, false, 0, true, nil
	}
	if maxBytes < len(key) {
		return nil, false, 0, true, nil
	}
	entries, more, err := reader.Scan(key, nil, 1, maxBytes)
	if err != nil {
		return nil, false, 0, false, mapStorePhysicalError(err)
	}
	defer clearBoltEntries(entries)
	if len(entries) == 0 {
		if more {
			return nil, false, len(key), true, nil
		}
		return nil, false, 0, false, nil
	}
	if !bytes.Equal(entries[0].Key, key) {
		return nil, false, 0, false, ErrCorrupt
	}
	used := len(entries[0].Key) + len(entries[0].Value)
	return bytes.Clone(entries[0].Value), true, used, false, nil
}

func decodeSearchIndexState(reader bolt.Reader, store *Store, maxBytes int) (int, bool, bool, error) {
	plain, found, used, blocked, err := readBoundedIndexValue(reader, store.exactStateKey(), maxBytes)
	if err != nil || blocked || !found {
		return used, found, blocked, err
	}
	envelope := plain
	decoded, err := store.open(store.exactStateKey(), store.exactNamespace("search-index-state-v1"), envelope)
	clear(envelope)
	// The observation index is rebuilt on a content-version bump, so its marker
	// must be current. The capture-part index is not rebuilt: its marker may
	// still carry the structure version it was written at, and its pages stay
	// decodable (see capturePartIndexStateReader).
	current := len(decoded) == 2 && (decoded[0] == searchIndexFormatVersion ||
		store.captureSearch && decoded[0] >= searchIndexStructureVersion && decoded[0] < searchIndexFormatVersion)
	if err != nil || !current || decoded[1] != 1 {
		clear(decoded)
		return used, false, false, ErrCorrupt
	}
	clear(decoded)
	return used, true, false, nil
}

func readSearchIndexReverse(reader bolt.Reader, store *Store, sourceID string, maxBytes int) (int, []searchIndexRef, bool, int, bool, error) {
	key := store.searchReverseKey(sourceID)
	encoded, found, used, blocked, err := readBoundedIndexValue(reader, key, maxBytes)
	if err != nil || blocked {
		clear(key)
		return 0, nil, false, used, blocked, err
	}
	if !found {
		clear(key)
		return 0, nil, false, used, false, ErrCorrupt
	}
	plain, err := store.open(key, store.exactNamespace("search-index-reverse-v4"), encoded)
	clear(key)
	clear(encoded)
	if err != nil {
		return 0, nil, false, used, false, ErrCorrupt
	}
	paged := len(plain) > 0 && plain[0] == searchPagedReverseVersion
	sourceBytes, refs, err := decodeSearchIndexReverse(plain)
	clear(plain)
	return sourceBytes, refs, paged, used, false, err
}

func findSearchIndexRef(refs []searchIndexRef, kind byte, digest [sha256.Size]byte) (searchIndexRef, bool) {
	position := sort.Search(len(refs), func(i int) bool {
		if refs[i].kind != kind {
			return refs[i].kind >= kind
		}
		return bytes.Compare(refs[i].digest[:], digest[:]) >= 0
	})
	if position < len(refs) && refs[position].kind == kind && refs[position].digest == digest {
		return refs[position], true
	}
	return searchIndexRef{}, false
}

func (s *Store) SearchExact(ctx context.Context, query search.ExactQuery) (search.ExactPage, error) {
	if ctx == nil || len(query.Terms) == 0 || len(query.Terms) > search.MaxTerms || query.MinTermMatches < 0 || query.MinTermMatches > len(query.Terms) || query.PartialOnly && (query.MinTermMatches == 0 || query.MinTermMatches >= len(query.Terms)) || query.TopK < 0 || query.TopK > search.MaxResults || query.MaxRows < 1 || query.MaxRows > search.MaxIndexRows || query.MaxBytes < 1 || query.MaxBytes > search.MaxIndexBytes {
		return search.ExactPage{}, search.ErrInvalidRequest
	}
	if query.TopK == 0 {
		query.TopK = search.MaxResults
	}
	for i, term := range query.Terms {
		normalized, err := search.NormalizeTokens(term, 1)
		if err != nil || len(normalized) != 1 || normalized[0] != term || i > 0 && query.Terms[i-1] >= term {
			return search.ExactPage{}, search.ErrInvalidRequest
		}
	}
	appID, err := search.CanonicalAppID(query.AppID)
	if err != nil || appID != query.AppID {
		return search.ExactPage{}, search.ErrInvalidRequest
	}
	domain, err := search.CanonicalDomain(query.Domain)
	if err != nil || domain != query.Domain {
		return search.ExactPage{}, search.ErrInvalidRequest
	}
	if query.CapturedStart.IsZero() != query.CapturedEnd.IsZero() || len(query.ScopeSourceIDs) > search.MaxScopeSourceIDs || len(query.ExcludeAppIDs) > search.MaxExcludedApps {
		return search.ExactPage{}, search.ErrInvalidRequest
	}
	if !query.CapturedStart.IsZero() && (!validCaptureTime(query.CapturedStart) || !validCaptureTime(query.CapturedEnd) || !query.CapturedStart.Before(query.CapturedEnd) || query.CapturedEnd.Sub(query.CapturedStart) > storage.MaxObservationWindow) {
		return search.ExactPage{}, search.ErrInvalidRequest
	}
	if len(query.ScopeSourceIDs) > 0 && query.CapturedStart.IsZero() {
		return search.ExactPage{}, search.ErrInvalidRequest
	}
	for _, id := range query.ScopeSourceIDs {
		if !validHash(id) {
			return search.ExactPage{}, search.ErrInvalidRequest
		}
	}
	for _, raw := range query.ExcludeAppIDs {
		app, err := search.CanonicalAppID(raw)
		if err != nil || app == "" || app != raw {
			return search.ExactPage{}, search.ErrInvalidRequest
		}
	}
	var result search.ExactPage
	err = s.view(ctx, func(reader bolt.Reader) error { // one snapshot for every posting and source reference
		return s.searchExactAllReader(ctx, reader, query, &result)
	})
	if err != nil {
		if errors.Is(err, ErrTransactionBudget) || errors.Is(err, bolt.ErrBudget) {
			return search.ExactPage{}, search.ErrIndexBudget
		}
		return search.ExactPage{}, err
	}
	return result, nil
}

type searchIndexSearchStream struct {
	constraintIndex int
	iterator        *searchIndexTreeIterator
	current         searchIndexPosting
	currentScore    int
	exhausted       bool
	term            bool // a term stream may exhaust without ending a partial-match query
	seen            map[string]struct{}
}

type searchIndexEvaluatedCandidate struct {
	fields       []byte
	matches      bool
	termMatches  int
	metadataOkay bool
	score        int
	termMask     uint32 // bit i is set when query term i is indexed for the source
}

func (s *Store) searchExactTreeReader(ctx context.Context, reader bolt.Reader, query search.ExactQuery, result *search.ExactPage) error {
	return s.searchExactTreeReaderScoped(ctx, reader, query, result, false)
}

func (s *Store) searchExactTreeReaderScoped(ctx context.Context, reader bolt.Reader, query search.ExactQuery, result *search.ExactPage, captureEvidence bool) error {
	used, ready, blocked, err := decodeSearchIndexState(reader, s, query.MaxBytes-result.Bytes)
	result.Bytes += used
	result.Nodes++
	if err != nil {
		return err
	}
	if blocked {
		return search.ErrIndexBudget
	}
	if !ready {
		return search.ErrIndexUnavailable
	}
	pendingID, pendingBytes, stopped, err := s.firstPendingSearchReader(reader, query.MaxBytes-result.Bytes)
	result.Nodes++
	result.Bytes += pendingBytes
	if err != nil {
		return err
	}
	if stopped {
		return search.ErrIndexBudget
	}
	result.IndexingPending = result.IndexingPending || pendingID != ""
	if captureEvidence {
		pending, err := s.captureIndexPendingReader(captureQueryReader{reader, result, query.MaxBytes})
		if err != nil {
			return err
		}
		result.IndexingPending = result.IndexingPending || pending
	}
	excluded := make([][sha256.Size]byte, 0, len(query.ExcludeAppIDs))
	for _, app := range query.ExcludeAppIDs {
		excluded = append(excluded, s.searchLookupDigest(searchIndexApp, app))
	}
	scopeSources := make(map[string]struct{}, len(query.ScopeSourceIDs))
	for _, id := range query.ScopeSourceIDs {
		scopeSources[id] = struct{}{}
	}
	constraints := make([]searchIndexConstraint, 0, len(query.Terms)+2)
	defer func() { clearSearchIndexConstraints(constraints) }()
	for _, term := range query.Terms {
		constraints = append(constraints, searchIndexConstraint{kind: searchIndexTerm, digest: s.searchLookupDigest(searchIndexTerm, term)})
	}
	if query.AppID != "" {
		constraints = append(constraints, searchIndexConstraint{kind: searchIndexApp, digest: s.searchLookupDigest(searchIndexApp, query.AppID)})
	}
	if query.Domain != "" {
		constraints = append(constraints, searchIndexConstraint{kind: searchIndexDomain, digest: s.searchLookupDigest(searchIndexDomain, query.Domain)})
	}
	streams := make([]searchIndexSearchStream, len(constraints))
	for i := range constraints {
		streams[i] = searchIndexSearchStream{
			term:            constraints[i].kind == searchIndexTerm,
			constraintIndex: i,
			iterator:        newSearchIndexTreeIterator(ctx, reader, s, constraints[i].kind, constraints[i].digest),
			seen:            make(map[string]struct{}),
		}
	}
	minTermMatches := query.MinTermMatches
	if minTermMatches == 0 {
		minTermMatches = len(query.Terms) // zero preserves exact all-term behavior
	}
	evaluated := make(map[string]searchIndexEvaluatedCandidate, min(query.MaxRows, 128))
	matchedCount := 0
	advance := func(streamIndex int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		stream := &streams[streamIndex]
		if stream.exhausted {
			return nil
		}
		if result.Rows >= query.MaxRows {
			return search.ErrIndexBudget
		}
		posting, ok, stopped, err := stream.iterator.next(result, query.MaxBytes)
		if err != nil {
			return err
		}
		if stopped {
			return search.ErrIndexBudget
		}
		if !ok {
			stream.exhausted = true
			stream.currentScore = 0
			return nil
		}
		result.Rows++
		if _, duplicate := stream.seen[posting.sourceID]; duplicate {
			return ErrCorrupt
		}
		stream.seen[posting.sourceID] = struct{}{}
		stream.current = posting
		constraint := constraints[stream.constraintIndex]
		if constraint.kind == searchIndexTerm {
			stream.currentScore = searchIndexWeight(posting.fields)
			if stream.currentScore == 0 {
				return ErrCorrupt
			}
		} else if posting.fields != 0 {
			return ErrCorrupt
		}
		if previous, seen := evaluated[posting.sourceID]; seen {
			if previous.fields[stream.constraintIndex] != posting.fields {
				return ErrCorrupt
			}
			return nil
		}
		if result.Nodes >= search.MaxIndexNodes {
			return search.ErrIndexBudget
		}
		result.Nodes++
		sourceBytes, refs, paged, readBytes, stopped, err := readSearchIndexReverse(reader, s, posting.sourceID, query.MaxBytes-result.Bytes)
		result.Bytes += readBytes
		if err != nil {
			return err
		}
		if stopped {
			return search.ErrIndexBudget
		}
		candidate := searchIndexEvaluatedCandidate{fields: make([]byte, len(constraints)), metadataOkay: true}
		for i, constraint := range constraints {
			ref, found := findSearchIndexRef(refs, constraint.kind, constraint.digest)
			if paged && constraint.kind == searchIndexTerm {
				if result.Nodes >= search.MaxIndexNodes {
					return search.ErrIndexBudget
				}
				result.Nodes++
				var readBytes int
				var stopped bool
				ref, found, readBytes, stopped, err = s.readSearchTermProof(reader, posting.sourceID, constraint.digest, query.MaxBytes-result.Bytes)
				result.Bytes += readBytes
				if err != nil {
					return err
				}
				if stopped {
					return search.ErrIndexBudget
				}
			}
			if !found {
				if constraint.kind != searchIndexTerm {
					candidate.metadataOkay = false
				}
				continue
			}
			candidate.fields[i] = ref.fields
			if constraint.kind == searchIndexTerm {
				candidate.termMatches++
				candidate.termMask |= 1 << uint(i)
				weight := searchIndexWeight(ref.fields)
				if weight == 0 {
					clearSearchIndexRefs(refs)
					return ErrCorrupt
				}
				candidate.score += weight
			} else if ref.fields != 0 {
				clearSearchIndexRefs(refs)
				return ErrCorrupt
			}
		}
		for _, digest := range excluded {
			if _, found := findSearchIndexRef(refs, searchIndexApp, digest); found {
				candidate.metadataOkay = false
				break
			}
		}
		clearSearchIndexRefs(refs)
		candidate.matches = candidate.metadataOkay && candidate.termMatches >= minTermMatches && (!query.PartialOnly || candidate.termMatches < len(query.Terms))
		if candidate.matches && !captureEvidence {
			current, err := s.isCurrentNoteSourceReader(reader, posting.sourceID)
			if err != nil {
				return err
			}
			candidate.matches = current
		}
		var capturedAt time.Time
		var captureEdition captureEditionRecord
		var captureSummary capture.CaptureSummary
		if captureEvidence && candidate.matches {
			var err error
			captureEdition, captureSummary, candidate.matches, err = s.captureQuerySource(reader, posting.sourceID, sourceBytes, result, query.MaxBytes)
			if err != nil {
				return err
			}
			if candidate.matches {
				capturedAt = captureSummary.Header().CapturedAt()
			}
		}
		if candidate.matches && !query.CapturedStart.IsZero() && captureEvidence {
			var visit time.Time
			if err := s.captureSightingInWindowBudget(ctx, reader, captureSummary.ContentID(), captureEdition, query.CapturedStart, query.CapturedEnd, &visit, result, query.MaxBytes); err != nil {
				return err
			}
			_, laterSighting := scopeSources[posting.sourceID]
			candidate.matches = laterSighting || !visit.IsZero()
			if !visit.IsZero() {
				capturedAt = visit
			}
		}
		if candidate.matches && !query.CapturedStart.IsZero() && !captureEvidence {
			if result.Nodes >= search.MaxIndexNodes {
				return search.ErrIndexBudget
			}
			result.Nodes++
			at, readBytes, blocked, err := s.readSearchCaptureTime(reader, posting.sourceID, sourceBytes, query.MaxBytes-result.Bytes)
			result.Bytes += readBytes
			if err != nil {
				return err
			}
			if blocked {
				return search.ErrIndexBudget
			}
			visit, found, err := s.sightingInWindow(posting.sourceID, at, query.CapturedStart, query.CapturedEnd, s.searchSightingReader(reader, result, query.MaxBytes))
			if err != nil {
				return err
			}
			capturedAt = at
			if !s.IsObservationTimeKnown(capturedAt) {
				capturedAt = time.Time{}
			}
			if found {
				capturedAt = visit
			}
			_, laterSighting := scopeSources[posting.sourceID]
			candidate.matches = laterSighting || found
		}
		if candidate.fields[stream.constraintIndex] != posting.fields {
			clear(candidate.fields)
			return ErrCorrupt
		}
		evaluated[posting.sourceID] = candidate
		if candidate.matches {
			matchedCount++
			result.Hits = append(result.Hits, search.IndexHit{SourceID: posting.sourceID, Score: candidate.score, Bytes: sourceBytes, CapturedAt: capturedAt, MatchedTerms: candidate.termMask})
			sort.Slice(result.Hits, func(i, j int) bool {
				if result.Hits[i].Score != result.Hits[j].Score {
					return result.Hits[i].Score > result.Hits[j].Score
				}
				return result.Hits[i].SourceID < result.Hits[j].SourceID
			})
			if len(result.Hits) > query.TopK {
				result.Hits = result.Hits[:query.TopK]
			}
		}
		return nil
	}
	for i := range streams {
		if err := advance(i); err != nil {
			return err
		}
		if streams[i].exhausted && searchIndexNoMoreMatches(streams, minTermMatches) {
			return nil
		}
	}
	for {
		if searchIndexTopKProven(result.Hits, query.TopK, streams, minTermMatches) {
			result.HasMore = matchedCount > len(result.Hits)
			for _, stream := range streams {
				result.HasMore = result.HasMore || !stream.exhausted
			}
			return nil
		}
		progressed := false
		for i := range streams {
			if streams[i].exhausted {
				continue
			}
			if err := advance(i); err != nil {
				return err
			}
			progressed = true
			if streams[i].exhausted && searchIndexNoMoreMatches(streams, minTermMatches) {
				result.HasMore = matchedCount > len(result.Hits)
				return nil
			}
		}
		if !progressed {
			result.HasMore = matchedCount > len(result.Hits)
			return nil
		}
	}
}

func (s *Store) readSearchCaptureTime(reader bolt.Reader, sourceID string, sourceBytes, maxBytes int) (time.Time, int, bool, error) {
	key := s.logicalKey(groupOrder, kindObservation, "", sourceID)
	defer clear(key)
	encoded, found, used, blocked, err := readBoundedIndexValue(reader, key, maxBytes)
	if err != nil || blocked {
		return time.Time{}, used, blocked, err
	}
	if !found {
		return time.Time{}, used, false, ErrCorrupt
	}
	defer clear(encoded)
	size, at, recordKey, err := s.decodeSizeEntry(key, encoded)
	defer clear(recordKey)
	if err != nil {
		return time.Time{}, used, false, err
	}
	expectedKey := s.logicalKey(groupRecord, kindObservation, "", sourceID)
	defer clear(expectedKey)
	if at.IsZero() || size != sourceBytes || !bytes.Equal(recordKey, expectedKey) {
		return time.Time{}, used, false, ErrCorrupt
	}
	return at, used, false, nil
}

// searchIndexNoMoreMatches reports that no unseen source can satisfy the query.
// Every source in an exhausted stream was already evaluated by random access,
// so an unseen source can only match through streams that still have postings.
// An exhausted metadata stream ends the query; an exhausted term stream only
// does so once fewer than minTermMatches term streams remain.
func searchIndexNoMoreMatches(streams []searchIndexSearchStream, minTermMatches int) bool {
	active := 0
	for _, stream := range streams {
		if !stream.term {
			if stream.exhausted {
				return true
			}
			continue
		}
		if !stream.exhausted {
			active++
		}
	}
	return active < minTermMatches
}

func searchIndexTopKProven(hits []search.IndexHit, topK int, streams []searchIndexSearchStream, minTermMatches int) bool {
	if len(hits) < topK {
		return false
	}
	if searchIndexNoMoreMatches(streams, minTermMatches) {
		return true
	}
	terms := 0
	for _, stream := range streams {
		if stream.term {
			terms++
		}
	}
	kth := hits[topK-1]
	if minTermMatches >= terms {
		// All-term query: an unseen source sits at or after every cursor.
		threshold := 0
		maxCursorID := ""
		for _, stream := range streams {
			threshold += stream.currentScore
			if stream.current.sourceID > maxCursorID {
				maxCursorID = stream.current.sourceID
			}
		}
		if kth.Score > threshold {
			return true
		}
		return kth.Score == threshold && kth.SourceID <= maxCursorID
	}
	// Partial query: an unseen source appears in at least minTermMatches of the
	// remaining term streams, so its score cannot exceed their largest cursor
	// scores, and (on a tie) its ID cannot precede the earliest cursor.
	var scores []int
	minCursorID := ""
	for _, stream := range streams {
		if !stream.term || stream.exhausted {
			continue
		}
		scores = append(scores, stream.currentScore)
		if minCursorID == "" || stream.current.sourceID < minCursorID {
			minCursorID = stream.current.sourceID
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(scores)))
	threshold := 0
	for _, score := range scores[:min(minTermMatches, len(scores))] {
		threshold += score
	}
	if kth.Score > threshold {
		return true
	}
	return kth.Score == threshold && kth.SourceID <= minCursorID
}

func clearSearchIndexConstraints(constraints []searchIndexConstraint) {
	for i := range constraints {
		clear(constraints[i].digest[:])
	}
}

func clearSearchIndexRefs(refs []searchIndexRef) {
	for i := range refs {
		clear(refs[i].digest[:])
	}
}
