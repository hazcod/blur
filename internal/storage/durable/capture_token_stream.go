package durable

import (
	"bytes"
	"context"
	"encoding/binary"
	"sort"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
)

// All work, including the unfinished token digest, commits with its exact input
// offset. A token may span any number of original parts without accumulating
// its text or restarting that token after a process restart.
func (s *Store) indexCaptureStreamRound(ctx context.Context, snapshot captureIndexSnapshot) error {
	return s.captureUpdate(ctx, func(tx *Tx) error {
		r, summary, live, err := s.captureLiveReader(tx.writer, snapshot.id)
		if err != nil {
			return err
		}
		if r.Version == 0 || !r.IndexQueued || r.Publication != snapshot.publication || r.IndexPart != snapshot.index || r.IndexFormat != snapshot.format {
			return nil
		}
		if !live {
			if err := tx.unlinkCaptureIndexLocked(snapshot.id, &r); err != nil {
				return err
			}
			return tx.capturePutJSONLocked("edition:"+snapshot.id, r, captureMetaBytes)
		}
		if r.IndexFormat != captureIndexContentFormat {
			if err := tx.queueCaptureIndexLocked(snapshot.id, &r, summary.PartCount()); err != nil {
				return err
			}
			return tx.capturePutJSONLocked("edition:"+snapshot.id, r, captureMetaBytes)
		}
		if r.IndexRangeWork != nil {
			if err := tx.advanceCaptureRangeWork(snapshot.id, &r, summary); err != nil {
				return err
			}
			return tx.capturePutJSONLocked("edition:"+snapshot.id, r, captureMetaBytes)
		}
		if r.IndexRangeQueued > 0 {
			if err := tx.nextCaptureRange(snapshot.id, &r, summary); err != nil {
				return err
			}
			return tx.capturePutJSONLocked("edition:"+snapshot.id, r, captureMetaBytes)
		}
		if r.IndexPart < summary.PartCount() {
			part, err := s.captureMemberPartReader(tx.writer, r, summary, r.IndexPart)
			if err != nil {
				return err
			}
			ready, err := tx.prepareCaptureStreamPart(snapshot.id, r, summary, part)
			if err != nil {
				return err
			}
			if ready < 4 {
				return nil
			}
			stream := search.CaptureTokenStream{}
			if r.IndexStream != nil {
				stream = *r.IndexStream
			}
			if !stream.Valid() || stream.Next < part.Offset() || stream.Next > part.Offset()+int64(len(part.Text())) {
				return ErrCorrupt
			}
			offset := int(stream.Next - part.Offset())
			used := 0
			var tokens []search.CaptureToken
			if !stream.Complete {
				used, tokens, err = feedCaptureIndexTokens(&stream, part.Text()[offset:], stream.Next, part.Index(), part.Index() == summary.PartCount()-1, ready)
				if err != nil {
					return ErrCorrupt
				}
			} else if offset != len(part.Text()) || part.Index() != summary.PartCount()-1 {
				return ErrCorrupt
			}
			if err := tx.appendCaptureTokens(snapshot.id, &r, summary, part, tokens); err != nil {
				return err
			}
			r.IndexStream = &stream
			if offset+used == len(part.Text()) && (part.Index() < summary.PartCount()-1 || stream.Complete) && r.IndexRangeWork == nil && r.IndexRangeQueued == 0 {
				work, err := tx.captureBoundaryIdentifierWork(r, summary, part)
				if err != nil {
					return err
				}
				r.IndexRangeWork = work
				r.IndexPart++
			}
		}
		if err := tx.indexNextCaptureVisitLocked(snapshot.id, &r, summary.Header().FamilyID()); err != nil {
			return err
		}
		if r.IndexPart == summary.PartCount() && r.IndexVisits == r.Visits && r.IndexRangeWork == nil && r.IndexRangeQueued == 0 {
			if err := tx.unlinkCaptureIndexLocked(snapshot.id, &r); err != nil {
				return err
			}
			r.IndexStream = nil
		}
		return tx.capturePutJSONLocked("edition:"+snapshot.id, r, captureMetaBytes)
	})
}

// Repeated filler must not turn one part into hundreds of transactions. Scan
// at most the current bounded part, retaining at most62 distinct token proofs.
func feedCaptureIndexTokens(stream *search.CaptureTokenStream, text string, offset, part int64, final bool, limit int) (int, []search.CaptureToken, error) {
	used := 0
	var out []search.CaptureToken
	type tokenKey struct {
		digest             [32]byte
		first, last, start int64
	}
	seen := make(map[tokenKey]bool)
	for !stream.Complete && len(out) <= limit-4 {
		n, tokens, err := stream.Feed(text[used:], offset+int64(used), part, final, limit-len(out))
		if err != nil {
			return 0, nil, err
		}
		used += n
		for _, token := range tokens {
			key := tokenKey{digest: token.Digest, first: token.FirstPart, last: token.LastPart}
			if token.FirstPart != token.LastPart {
				key.start = token.Start
			}
			if !seen[key] {
				seen[key] = true
				out = append(out, token)
			}
		}
		if used == len(text) && (!final || stream.Complete) {
			break
		}
		if n == 0 && len(tokens) == 0 {
			return 0, nil, ErrCorrupt
		}
	}
	return used, out, nil
}

func (tx *Tx) prepareCaptureStreamPart(id string, r captureEditionRecord, summary capture.CaptureSummary, part capture.CapturePart) (int, error) {
	sourceID := capturePartSourceID(id, r.Publication, part.Index())
	mapping, found, err := tx.store.captureIndexPartReader(tx.writer, sourceID)
	if err != nil {
		return 0, err
	}
	if found && (mapping.Range || mapping.EditionID != id || mapping.Publication != r.Publication || mapping.Index != part.Index() || mapping.Bytes != len(part.Text())) {
		return 0, ErrCorrupt
	}
	if !found || mapping.Format != captureIndexContentFormat {
		if found {
			done, err := tx.eraseCapturePartIndexLocked(id, r, part.Index())
			if err != nil || !done {
				return 0, err
			}
		}
		mapping = captureIndexPart{Version: captureIndexVersion, EditionID: id, Publication: r.Publication, Index: part.Index(), Bytes: len(part.Text()), Format: captureIndexContentFormat}
		if err := tx.initializeCapturePartIndexLocked(sourceID, len(part.Text())+len(summary.Header().AppID())+len(summary.Header().Domain()), mapping); err != nil {
			return 0, err
		}
	}
	metadata, err := searchFieldTermsLimit("", summary.Header().AppID(), summary.Header().Domain(), "", 128+253)
	if err != nil {
		return 0, err
	}
	ordered := sortedTermKeys(metadata)
	if mapping.MetadataIndexed > len(ordered) {
		return 0, ErrCorrupt
	}
	if mapping.MetadataIndexed == len(ordered) {
		return exactIndexBatchTerms, nil
	}
	// Preserve exact metadata/text scoring before streaming text admission.
	raw, err := searchFieldTermsLimit(part.Text(), summary.Header().AppID(), summary.Header().Domain(), "", capture.MaxCapturePartTextBytes+128+253)
	if err != nil {
		return 0, err
	}
	before := mapping.MetadataIndexed
	end := min(len(ordered), mapping.MetadataIndexed+exactIndexBatchTerms)
	digests := make(map[[32]byte]byte, end-mapping.MetadataIndexed)
	for _, term := range ordered[mapping.MetadataIndexed:end] {
		digests[tx.store.capturePartIndex().searchLookupDigest(searchIndexTerm, term)] = raw[term]
	}
	if err := tx.capturePartSearch().appendCaptureDigests(sourceID, summary.Header().AppID(), summary.Header().Domain(), digests); err != nil {
		return 0, err
	}
	mapping.MetadataIndexed = end
	if err := tx.capturePutJSONLocked("index-part:"+sourceID, mapping, 768); err != nil {
		return 0, err
	}
	if end == len(ordered) {
		return exactIndexBatchTerms - (end - before), nil
	}
	return 0, nil
}

func (tx *Tx) appendCaptureTokens(id string, r *captureEditionRecord, summary capture.CaptureSummary, part capture.CapturePart, tokens []search.CaptureToken) error {
	if err := tx.queueCaptureRanges(id, r, summary, tokens); err != nil {
		return err
	}
	normal := make(map[int64]map[[32]byte]byte)
	for _, token := range tokens {
		digest := tx.store.capturePartIndex().captureTermLookupDigest(token.Digest)
		if token.FirstPart == token.LastPart {
			if normal[token.FirstPart] == nil {
				normal[token.FirstPart] = make(map[[32]byte]byte)
			}
			normal[token.FirstPart][digest] |= searchFieldText
			continue
		}
	}
	for index, terms := range normal {
		if err := tx.capturePartSearch().appendCaptureDigests(capturePartSourceID(id, r.Publication, index), summary.Header().AppID(), summary.Header().Domain(), terms); err != nil {
			return err
		}
	}
	return nil
}

// Append unique authenticated token digests to reverse pages. No word string
// or whole-source term list is retained. Repeated words only update field bits.
func (tx *searchIndexTx) appendCaptureDigests(id, app, domain string, terms map[[32]byte]byte) error {
	rootKey := tx.store.searchReverseKey(id)
	defer clear(rootKey)
	root, found, err := tx.searchIndexPlainLocked(rootKey, "search-index-reverse-v4")
	defer clear(root)
	if err != nil {
		return err
	}
	if !found {
		return ErrCorrupt
	}
	progress, sourceBytes, refs, err := decodePagedSearchReverse(root)
	if err != nil || progress.ErasedPages != 0 {
		return ErrCorrupt
	}
	if len(refs) == 0 {
		for _, field := range []struct {
			kind  byte
			value string
		}{{searchIndexApp, app}, {searchIndexDomain, domain}} {
			if field.value == "" {
				continue
			}
			page, digest, err := tx.addSearchIndexPostingLocked(field.kind, field.value, id, 0)
			if err != nil {
				return err
			}
			refs = append(refs, searchIndexRef{kind: field.kind, digest: digest, page: page})
		}
	}
	type changedPage struct {
		refs    []searchIndexRef
		existed bool
	}
	pages := make(map[uint32]*changedPage)
	loadPage := func(ordinal uint32) (*changedPage, error) {
		if page := pages[ordinal]; page != nil {
			return page, nil
		}
		page := &changedPage{}
		pages[ordinal] = page
		if uint64(ordinal)*exactIndexBatchTerms < uint64(progress.Indexed) {
			key := tx.store.searchSourcePageKey(id, ordinal)
			encoded, found, err := tx.searchIndexPlainLocked(key, "search-source-page-v1")
			clear(key)
			if err != nil || !found {
				return nil, ErrCorrupt
			}
			_, page.refs, err = decodeSearchIndexReverse(encoded)
			clear(encoded)
			if err != nil {
				return nil, err
			}
			page.existed = true
		}
		return page, nil
	}
	ordered := make([][32]byte, 0, len(terms))
	for digest := range terms {
		ordered = append(ordered, digest)
	}
	sort.Slice(ordered, func(i, j int) bool { return bytes.Compare(ordered[i][:], ordered[j][:]) < 0 })
	for _, digest := range ordered {
		key := tx.store.searchProofKey(id, digest)
		plain, exists, err := tx.searchIndexPlainLocked(key, "search-proof-v1")
		if err != nil {
			return err
		}
		fields := terms[digest]
		ordinal := progress.Indexed
		oldFields := byte(0)
		if exists {
			if len(plain) != 6 || plain[0] != 2 || plain[1] == 0 || plain[1]&^searchFieldMask != 0 {
				return ErrCorrupt
			}
			oldFields = plain[1]
			fields |= oldFields
			ordinal = binary.BigEndian.Uint32(plain[2:])
			if ordinal >= progress.Indexed {
				return ErrCorrupt
			}
			if fields == oldFields {
				clear(plain)
				clear(key)
				continue
			}
		}
		clear(plain)
		page, err := loadPage(ordinal / exactIndexBatchTerms)
		if err != nil {
			return err
		}
		if exists {
			if err := tx.removeSearchIndexTreePostingLocked(searchIndexTerm, digest, id, oldFields); err != nil {
				return err
			}
			updated := false
			for i, ref := range page.refs {
				if ref.digest == digest {
					if ref.fields != oldFields {
						return ErrCorrupt
					}
					page.refs[i].fields = fields
					updated = true
					break
				}
			}
			if !updated {
				return ErrCorrupt
			}
		} else {
			if progress.Indexed == ^uint32(0) {
				return ErrCorrupt
			}
			page.refs = append(page.refs, searchIndexRef{kind: searchIndexTerm, digest: digest, fields: fields})
			progress.Indexed++
			progress.Total = progress.Indexed
		}
		order, err := searchIndexOrderKey(searchIndexTerm, fields, id)
		if err != nil {
			return err
		}
		if err := tx.insertSearchIndexTreeLocked(searchIndexTerm, digest, order, searchIndexPosting{id, fields}); err != nil {
			return err
		}
		proof := []byte{2, fields, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(proof[2:], ordinal)
		if err := tx.putSearchIndexValueLocked(key, "search-proof-v1", proof, exists); err != nil {
			return err
		}
		clear(key)
	}
	for ordinal, page := range pages {
		key := tx.store.searchSourcePageKey(id, ordinal)
		encoded, err := encodeSearchIndexReverse(0, page.refs)
		if err != nil {
			return err
		}
		err = tx.putSearchIndexValueLocked(key, "search-source-page-v1", encoded, page.existed)
		clear(key)
		clear(encoded)
		if err != nil {
			return err
		}
	}
	encoded, err := encodePagedSearchReverse(progress, sourceBytes, refs)
	if err != nil {
		return err
	}
	defer clear(encoded)
	return tx.putSearchIndexValueLocked(rootKey, "search-index-reverse-v4", encoded, true)
}
