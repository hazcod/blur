package durable

import (
	"context"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// Capture metadata shares the original query's node and byte budgets. Get
// first uses a bounded exact-key read, so oversize records cannot allocate past
// the budget before being rejected. It never scans an unbounded directory.
type captureQueryReader struct {
	bolt.Reader
	page     *search.ExactPage
	maxBytes int
}

func (r captureQueryReader) Get(key []byte) ([]byte, bool, error) {
	if r.page.Nodes >= search.MaxIndexNodes {
		return nil, false, bolt.ErrBudget
	}
	r.page.Nodes++
	value, found, used, blocked, err := readBoundedIndexValue(r.Reader, key, r.maxBytes-r.page.Bytes)
	r.page.Bytes += used
	if err != nil {
		return nil, false, err
	}
	if blocked {
		return nil, false, bolt.ErrBudget
	}
	return value, found, nil
}
func (r captureQueryReader) Has(key []byte) (bool, error) {
	value, found, err := r.Get(key)
	clear(value)
	return found, err
}

func (s *Store) captureQuerySource(reader bolt.Reader, id string, sourceBytes int, page *search.ExactPage, maxBytes int) (captureEditionRecord, capture.CaptureSummary, bool, error) {
	bounded := captureQueryReader{reader, page, maxBytes}
	mapping, found, err := s.captureIndexPartReader(bounded, id)
	if err != nil {
		return captureEditionRecord{}, capture.CaptureSummary{}, false, err
	}
	if !found {
		return captureEditionRecord{}, capture.CaptureSummary{}, false, ErrCorrupt
	}
	edition, summary, live, err := s.captureLiveReader(bounded, mapping.EditionID)
	if err != nil || !live {
		return edition, summary, false, err
	}
	if edition.Publication != mapping.Publication {
		return edition, summary, false, nil
	}
	expected := mapping.Bytes + len(summary.Header().AppID()) + len(summary.Header().Domain())
	if expected != sourceBytes {
		return edition, summary, false, ErrCorrupt
	}
	return edition, summary, true, nil
}

func (s *Store) searchExactAllReader(ctx context.Context, reader bolt.Reader, query search.ExactQuery, result *search.ExactPage) error {
	if err := s.searchExactTreeReader(ctx, reader, query, result); err != nil {
		return err
	}
	if s.captureSearch {
		return nil
	}
	if result.Nodes >= search.MaxIndexNodes {
		return search.ErrIndexBudget
	}
	result.Nodes++
	catalogKey := s.logicalKey(groupRecord, kindIndex, "index:"+captureNS, "published")
	exists, err := reader.Has(catalogKey)
	clear(catalogKey)
	if err != nil {
		return mapStorePhysicalError(err)
	}
	if !exists {
		return nil
	}
	priorMore, priorPending := result.HasMore, result.IndexingPending
	if err := s.capturePartIndex().searchExactTreeReaderScoped(ctx, reader, query, result, true); err != nil {
		return err
	}
	result.HasMore = result.HasMore || priorMore
	result.IndexingPending = result.IndexingPending || priorPending
	return nil
}
