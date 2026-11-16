package durable

// indexSearchTermsBatchLocked advances one shared64-term batch. The caller
// owns source admission, the initial paged reverse row and its work receipt.
func (tx *searchIndexTx) indexSearchTermsBatchLocked(id, app, domain string, expectedBytes int, terms map[string]byte, ordered []string) (bool, error) {

	rootKey := tx.store.searchReverseKey(id)
	defer clear(rootKey)
	root, found, err := tx.searchIndexPlainLocked(rootKey, "search-index-reverse-v4")
	defer clear(root)
	if err != nil {
		return false, err
	}
	if !found {
		return false, ErrCorrupt
	}
	progress, sourceBytes, refs, err := decodePagedSearchReverse(root)
	if err != nil {
		return false, err
	}
	if progress.ErasedPages != 0 || sourceBytes != expectedBytes {
		return false, ErrCorrupt
	}
	if progress.Total != 0 && int(progress.Total) != len(ordered) {
		return false, ErrCorrupt
	}
	progress.Total = uint32(len(ordered))
	if progress.Indexed == 0 {
		// Metadata postings are tiny and join the first term batch atomically.
		for _, field := range []struct {
			kind  byte
			value string
		}{{searchIndexApp, app}, {searchIndexDomain, domain}} {
			if field.value == "" {
				continue
			}
			page, digest, err := tx.addSearchIndexPostingLocked(field.kind, field.value, id, 0)
			if err != nil {
				return false, tx.failLocked(err)
			}
			refs = append(refs, searchIndexRef{kind: field.kind, digest: digest, page: page})
		}
	}
	end := min(int(progress.Indexed)+exactIndexBatchTerms, len(ordered))
	pageRefs := make([]searchIndexRef, 0, end-int(progress.Indexed))
	for _, term := range ordered[int(progress.Indexed):end] {
		_, digest, err := tx.addSearchIndexPostingLocked(searchIndexTerm, term, id, terms[term])
		if err != nil {
			return false, tx.failLocked(err)
		}
		ref := searchIndexRef{kind: searchIndexTerm, digest: digest, fields: terms[term]}
		pageRefs = append(pageRefs, ref)
		key := tx.store.searchProofKey(id, digest)
		err = tx.putSearchIndexValueLocked(key, "search-proof-v1", []byte{1, ref.fields}, false)
		clear(key)
		if err != nil {
			return false, tx.failLocked(err)
		}
	}
	if len(pageRefs) > 0 {
		pageKey := tx.store.searchSourcePageKey(id, progress.Indexed/exactIndexBatchTerms)
		encoded, err := encodeSearchIndexReverse(0, pageRefs)
		if err != nil {
			clear(pageKey)
			return false, err
		}
		err = tx.putSearchIndexValueLocked(pageKey, "search-source-page-v1", encoded, false)
		clear(pageKey)
		clear(encoded)
		if err != nil {
			return false, tx.failLocked(err)
		}
	}
	progress.Indexed = uint32(end)
	encoded, err := encodePagedSearchReverse(progress, sourceBytes, refs)
	if err != nil {
		return false, err
	}
	err = tx.putSearchIndexValueLocked(rootKey, "search-index-reverse-v4", encoded, true)
	clear(encoded)
	if err != nil {
		return false, tx.failLocked(err)
	}
	return end == len(ordered), nil
}
