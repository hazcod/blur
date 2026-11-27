package durable

import (
	"context"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// ensureCapturePartIndex initializes only bounded metadata. Existing editions
// enter the durable backfill cursor; opening never tokenizes all their parts.
func (s *Store) ensureCapturePartIndex(ctx context.Context) error {
	// Even an empty bbolt write commits new file metadata. Authenticate the
	// completed initialization with reads so ready stores reopen unchanged.
	ready := false
	err := s.view(ctx, func(reader bolt.Reader) error {
		format, err := s.capturePartIndexStateReader(reader)
		if err != nil {
			return err
		}
		backfill, backfillFound, err := s.captureIndexBackfillReader(reader)
		if err != nil {
			return err
		}
		marker, timelineFound, err := s.readIndexValueReader(reader, captureNS, captureTimelineMarkerID, 1)
		if err == nil && timelineFound && (len(marker) != 1 || marker[0] != 1) {
			err = ErrCorrupt
		}
		clear(marker)
		ready = format == captureIndexContentFormat && backfillFound && backfill.Format == captureIndexContentFormat && timelineFound
		return err
	})
	if err != nil || ready {
		return err
	}
	return s.captureUpdate(ctx, func(tx *Tx) error {
		scoped := tx.capturePartSearch()
		format, err := s.capturePartIndexStateReader(tx.writer)
		if err != nil {
			return err
		}
		if format != captureIndexContentFormat {
			if err := scoped.putSearchIndexValueLocked(scoped.store.exactStateKey(), "search-index-state-v1", []byte{captureIndexContentFormat, 1}, format != 0); err != nil {
				return err
			}
		}
		backfill, exists, err := s.captureIndexBackfillReader(tx.writer)
		if err != nil {
			return err
		}
		marker, timelineFound, err := tx.readIndexValueLocked(captureNS, captureTimelineMarkerID, 1)
		if err != nil {
			return err
		}
		if timelineFound && (len(marker) != 1 || marker[0] != 1) {
			clear(marker)
			return ErrCorrupt
		}
		clear(marker)
		if exists && backfill.Format == captureIndexContentFormat && format == captureIndexContentFormat && timelineFound {
			return nil
		}
		catalog, err := s.captureCatalogReader(tx.writer, "published")
		if err != nil {
			return err
		}
		if err := tx.capturePutJSONLocked(captureIndexBackfillKey, captureIndexBackfill{Version: captureIndexVersion, Next: catalog.Head, Format: captureIndexContentFormat}, 256); err != nil {
			return err
		}
		return tx.putIndexValueLocked(captureNS, captureTimelineMarkerID, []byte{1})
	})
}

// Older content formats remain structurally readable while the durable
// backfill and part queue migrate their derived tokens in the background.
func (s *Store) capturePartIndexStateReader(reader bolt.Reader) (int, error) {
	index := s.capturePartIndex()
	key := index.exactStateKey()
	envelope, found, err := reader.Get(key)
	if err != nil {
		return 0, mapStorePhysicalError(err)
	}
	if !found {
		return 0, nil
	}
	plain, err := index.open(key, index.exactNamespace("search-index-state-v1"), envelope)
	clear(envelope)
	defer clear(plain)
	if err != nil || len(plain) != 2 || plain[0] < searchIndexStructureVersion || plain[0] > captureIndexContentFormat || plain[1] != 1 {
		return 0, ErrCorrupt
	}
	return int(plain[0]), nil
}

func (s *Store) captureIndexPendingReader(reader bolt.Reader) (bool, error) {
	backfill, found, err := s.captureIndexBackfillReader(reader)
	if err != nil {
		return false, err
	}
	if !found {
		return false, ErrCorrupt
	}
	root, err := s.captureCatalogReader(reader, captureIndexPendingKey)
	return backfill.Next != "" || root.Count > 0, err
}
func (s *Store) CaptureIndexPending(ctx context.Context) (bool, error) {
	var pending bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		pending, err = s.captureIndexPendingReader(reader)
		return err
	})
	return pending, err
}
func (s *Store) advanceCaptureIndexBackfill(ctx context.Context) error {
	return s.captureUpdate(ctx, func(tx *Tx) error {
		backfill, found, err := s.captureIndexBackfillReader(tx.writer)
		if err != nil {
			return err
		}
		if !found {
			return ErrCorrupt
		}
		if backfill.Next == "" {
			return nil
		}
		id := backfill.Next
		r, summary, live, err := s.captureLiveReader(tx.writer, id)
		if err != nil {
			return err
		}
		if r.Version == 0 {
			return ErrCorrupt
		}
		backfill.Next = r.Next
		if live {
			if r.TimelineVersion == 0 {
				// Revisit only receipt metadata, not already indexed text. The
				// durable queue keeps this bounded migration restart-safe.
				r.IndexVisits, r.TimelineVersion = 0, 1
			}
			if err := tx.queueCaptureIndexLocked(id, &r, summary.PartCount()); err != nil {
				return err
			}
			if err := tx.capturePutJSONLocked("edition:"+id, r, captureMetaBytes); err != nil {
				return err
			}
		}
		return tx.capturePutJSONLocked(captureIndexBackfillKey, backfill, 256)
	})
}

// Check the bounded predecessor link and terminal summary, preserving original
// spans. Only authenticated read values enter indexing, never fragment redaction.
func (s *Store) captureMemberPartReader(reader bolt.Reader, edition captureEditionRecord, summary capture.CaptureSummary, index int64) (capture.CapturePart, error) {
	if index < 0 || index >= summary.PartCount() {
		return capture.CapturePart{}, ErrCorrupt
	}
	stored, part, err := s.capturePartReader(reader, edition.Owner, index)
	if err != nil {
		return capture.CapturePart{}, err
	}
	expectedRoot := summary.Header().InitialRoot()
	offset := int64(0)
	if index > 0 {
		previous, p, err := s.capturePartReader(reader, edition.Owner, index-1)
		if err != nil {
			return capture.CapturePart{}, err
		}
		expectedRoot = previous.After
		offset = p.Offset() + int64(len(p.Text()))
	}
	end := part.Offset() + int64(len(part.Text()))
	if stored.Before != expectedRoot || part.Offset() != offset || end > summary.Bytes() || index == summary.PartCount()-1 && (end != summary.Bytes() || stored.After != summary.Root()) {
		return capture.CapturePart{}, ErrCorrupt
	}
	for _, span := range part.Origins() {
		if span.InputEnd > summary.RawBytes() {
			return capture.CapturePart{}, ErrCorrupt
		}
	}
	return part, nil
}

type captureIndexSnapshot struct {
	id, publication string
	index           int64
	format          int
	part            capture.CapturePart
	summary         capture.CaptureSummary
	live            bool
}

func (s *Store) loadCaptureIndexSnapshot(ctx context.Context) (captureIndexSnapshot, error) {
	var snapshot captureIndexSnapshot
	err := s.view(ctx, func(reader bolt.Reader) error {
		root, err := s.captureCatalogReader(reader, captureIndexPendingKey)
		if err != nil || root.Head == "" {
			return err
		}
		r, summary, live, err := s.captureLiveReader(reader, root.Head)
		if err != nil {
			return err
		}
		if r.Version == 0 || !r.IndexQueued || r.IndexPrev != "" {
			return ErrCorrupt
		}
		snapshot = captureIndexSnapshot{id: root.Head, publication: r.Publication, index: r.IndexPart, format: r.IndexFormat, summary: summary, live: live}

		return err
	})
	return snapshot, err
}

// IndexPendingCaptures advances bounded work rounds. Each round has at most a
// backfill enqueue transaction and one64-term part batch plus one old visit.
// It never enters the observation-only exactInline post-commit optimization.
func (s *Store) IndexPendingCaptures(ctx context.Context, maxBatches int) (bool, error) {
	if ctx == nil || maxBatches < 1 || maxBatches > 1024 {
		return false, search.ErrInvalidRequest
	}
	for round := 0; round < maxBatches; round++ {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		if err := s.advanceCaptureIndexBackfill(ctx); err != nil {
			return true, err
		}
		snapshot, err := s.loadCaptureIndexSnapshot(ctx)
		if err != nil {
			return true, err
		}
		if snapshot.id == "" {
			pending, err := s.CaptureIndexPending(ctx)
			if err != nil || !pending {
				return pending, err
			}
			continue // a backfill step skipped a suppressed edition
		}
		if err := s.indexCaptureStreamRound(ctx, snapshot); err != nil {
			return true, err
		}
	}
	return s.CaptureIndexPending(ctx)
}
func (tx *Tx) initializeCapturePartIndexLocked(sourceID string, sourceBytes int, mapping captureIndexPart) error {
	scoped := tx.capturePartSearch()
	key := scoped.store.searchReverseKey(sourceID)
	defer clear(key)
	encoded, err := encodePagedSearchReverse(pagedSearchProgress{}, sourceBytes, nil)
	if err != nil {
		return err
	}
	defer clear(encoded)
	if err := scoped.putSearchIndexValueLocked(key, "search-index-reverse-v4", encoded, false); err != nil {
		return err
	}
	return tx.capturePutJSONLocked("index-part:"+sourceID, mapping, 768)
}

// A part's reverse pages carry their own bounded erasure cursor. The caller
// advances the shared source journal only once the index and payload can go.
func (tx *Tx) eraseCapturePartIndexLocked(id string, edition captureEditionRecord, index int64) (bool, error) {
	sourceID := capturePartSourceID(id, edition.Publication, index)
	scoped := tx.capturePartSearch()
	key := scoped.store.searchReverseKey(sourceID)
	found, err := tx.writer.Has(key)
	clear(key)
	if err != nil {
		return false, mapStorePhysicalError(err)
	}
	mapping, mapped, err := tx.store.captureIndexPartReader(tx.writer, sourceID)
	if err != nil {
		return false, err
	}
	if found != mapped {
		return false, ErrCorrupt
	}
	if !found {
		if edition.IndexVersion != 0 && index < edition.IndexPart {
			return false, ErrCorrupt
		}
		return true, nil
	}
	if mapping.EditionID != id || mapping.Publication != edition.Publication || mapping.Index != index {
		return false, ErrCorrupt
	}
	done, err := scoped.removePagedSearchBatchLocked(sourceID, true)
	if err != nil || !done {
		return false, err
	}
	if err := scoped.removeSearchIndexSourceLocked(sourceID); err != nil {
		return false, err
	}
	return true, tx.deleteIndexValueLocked(captureNS, "index-part:"+sourceID)
}
