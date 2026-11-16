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
		stateFound, err := s.capturePartIndexStateReader(reader)
		if err != nil {
			return err
		}
		_, backfillFound, err := s.captureIndexBackfillReader(reader)
		if err != nil {
			return err
		}
		marker, timelineFound, err := s.readIndexValueReader(reader, captureNS, captureTimelineMarkerID, 1)
		if err == nil && timelineFound && (len(marker) != 1 || marker[0] != 1) {
			err = ErrCorrupt
		}
		clear(marker)
		ready = stateFound && backfillFound && timelineFound
		return err
	})
	if err != nil || ready {
		return err
	}
	return s.captureUpdate(ctx, func(tx *Tx) error {
		scoped := tx.capturePartSearch()
		found, err := s.capturePartIndexStateReader(tx.writer)
		if err != nil {
			return err
		}
		if !found {
			if err := scoped.writeSearchIndexStateLocked(); err != nil {
				return err
			}
		}
		_, exists, err := s.captureIndexBackfillReader(tx.writer)
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
		if exists && timelineFound {
			return nil
		}
		catalog, err := s.captureCatalogReader(tx.writer, "published")
		if err != nil {
			return err
		}
		if err := tx.capturePutJSONLocked(captureIndexBackfillKey, captureIndexBackfill{captureIndexVersion, catalog.Head}, 256); err != nil {
			return err
		}
		return tx.putIndexValueLocked(captureNS, captureTimelineMarkerID, []byte{1})
	})
}

func (s *Store) capturePartIndexStateReader(reader bolt.Reader) (bool, error) {
	index := s.capturePartIndex()
	key := index.exactStateKey()
	envelope, found, err := reader.Get(key)
	if err != nil {
		return false, mapStorePhysicalError(err)
	}
	if !found {
		return false, nil
	}
	plain, err := index.open(key, index.exactNamespace("search-index-state-v1"), envelope)
	clear(envelope)
	defer clear(plain)
	if err != nil || len(plain) != 2 || plain[0] != searchIndexFormatVersion || plain[1] != 1 {
		return false, ErrCorrupt
	}
	return true, nil
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
		snapshot = captureIndexSnapshot{id: root.Head, publication: r.Publication, index: r.IndexPart, summary: summary, live: live}
		if live && r.IndexPart < summary.PartCount() {
			snapshot.part, err = s.captureMemberPartReader(reader, r, summary, r.IndexPart)
		}
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
		var terms map[string]byte
		var ordered []string
		sourceBytes := 0
		if snapshot.part.Text() != "" {
			terms, err = searchFieldTermsLimit(snapshot.part.Text(), snapshot.summary.Header().AppID(), snapshot.summary.Header().Domain(), capture.MaxCapturePartTextBytes+128+253)
			if err != nil {
				return true, err
			}
			ordered = sortedTermKeys(terms)
			sourceBytes = len(snapshot.part.Text()) + len(snapshot.summary.Header().AppID()) + len(snapshot.summary.Header().Domain())
		}
		err = s.captureUpdate(ctx, func(tx *Tx) error {
			r, summary, live, err := s.captureLiveReader(tx.writer, snapshot.id)
			if err != nil {
				return err
			}
			if r.Version == 0 || !r.IndexQueued || r.Publication != snapshot.publication || r.IndexPart != snapshot.index {
				return nil
			}
			if !live {
				if err := tx.unlinkCaptureIndexLocked(snapshot.id, &r); err != nil {
					return err
				}
				return tx.capturePutJSONLocked("edition:"+snapshot.id, r, captureMetaBytes)
			}
			if r.IndexPart < summary.PartCount() {
				if snapshot.part.Text() == "" {
					return nil
				} // a newly admitted visit revived this edition
				if err := tx.indexCapturePartBatchLocked(snapshot.id, &r, snapshot.part, summary, sourceBytes, terms, ordered); err != nil {
					return err
				}
			}
			if err := tx.indexNextCaptureVisitLocked(snapshot.id, &r, summary.Header().FamilyID()); err != nil {
				return err
			}
			if r.IndexPart == summary.PartCount() && r.IndexVisits == r.Visits {
				if err := tx.unlinkCaptureIndexLocked(snapshot.id, &r); err != nil {
					return err
				}
			}
			return tx.capturePutJSONLocked("edition:"+snapshot.id, r, captureMetaBytes)
		})
		if err != nil {
			return true, err
		}
	}
	return s.CaptureIndexPending(ctx)
}
func (tx *Tx) indexCapturePartBatchLocked(id string, r *captureEditionRecord, part capture.CapturePart, summary capture.CaptureSummary, sourceBytes int, terms map[string]byte, ordered []string) error {
	sourceID := capturePartSourceID(id, r.Publication, r.IndexPart)
	scoped := tx.capturePartSearch()
	key := scoped.store.searchReverseKey(sourceID)
	defer clear(key)
	root, found, err := scoped.searchIndexPlainLocked(key, "search-index-reverse-v4")
	clear(root)
	if err != nil {
		return err
	}
	mapping, mapped, err := tx.store.captureIndexPartReader(tx.writer, sourceID)
	if err != nil {
		return err
	}
	expected := captureIndexPart{captureIndexVersion, id, r.Publication, r.IndexPart, len(part.Text())}
	if found != mapped || mapped && mapping != expected {
		return ErrCorrupt
	}
	if !found {
		encoded, err := encodePagedSearchReverse(pagedSearchProgress{}, sourceBytes, nil)
		if err != nil {
			return err
		}
		if err := scoped.putSearchIndexValueLocked(key, "search-index-reverse-v4", encoded, false); err != nil {
			clear(encoded)
			return err
		}
		clear(encoded)
		if err := tx.capturePutJSONLocked("index-part:"+sourceID, expected, 512); err != nil {
			return err
		}
	}
	done, err := scoped.indexSearchTermsBatchLocked(sourceID, summary.Header().AppID(), summary.Header().Domain(), sourceBytes, terms, ordered)
	if err != nil {
		return err
	}
	if done {
		r.IndexPart++
	}
	return nil
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
