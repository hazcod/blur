package durable

import (
	"time"

	"github.com/hazcod/blur/internal/storage"
)

const (
	captureTimelineNS       = "capture-part-time"
	captureTimelineRefNS    = "capture-part-time-ref"
	captureTimelineMarkerID = "capture-timeline-v1"
)

// One encrypted metadata row represents an edition in an hour, irrespective of
// its number of parts or visits. Publication never expands a document here.
func (tx *Tx) indexCaptureTimelineLocked(id string, at time.Time) error {
	if !validHash(id) || !validCaptureTime(at) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	hourID := observationTimelineHourID(at)
	refID := id + ":" + hourID
	ref, found, err := tx.readIndexValueLocked(captureTimelineRefNS, refID, 64)
	if err != nil {
		return err
	}
	if found {
		pageID := string(ref)
		clear(ref)
		actualHour, _, err := parseObservationTimelinePageID(pageID)
		if err != nil || actualHour != hourID {
			return ErrCorrupt
		}
		encoded, found, err := tx.readIndexValueLocked(captureTimelineNS, pageID, maxObservationTimelinePage)
		if err != nil {
			return err
		}
		if !found {
			return ErrCorrupt
		}
		rows, err := decodeObservationTimelinePage(encoded, hourID)
		clear(encoded)
		if err != nil {
			return err
		}
		for i := range rows {
			if rows[i].id != id {
				continue
			}
			if !rows[i].at.Before(at) {
				return nil
			}
			rows[i].at = at.UTC()
			encoded = encodeObservationTimelinePage(rows)
			defer clear(encoded)
			return tx.putIndexValueLocked(captureTimelineNS, pageID, encoded)
		}
		return ErrCorrupt
	}
	encoded, found, err := tx.readIndexValueLocked(captureTimelineNS, hourID, 9)
	if err != nil {
		return err
	}
	meta := observationTimelineMeta{pages: 1}
	var rows []captureWindowCandidate
	if found {
		meta, err = decodeObservationTimelineMeta(encoded)
		clear(encoded)
		if err != nil {
			return err
		}
		encoded, found, err = tx.readIndexValueLocked(captureTimelineNS, observationTimelinePageID(hourID, meta.pages-1), maxObservationTimelinePage)
		if err != nil {
			return err
		}
		if !found {
			return ErrCorrupt
		}
		rows, err = decodeObservationTimelinePage(encoded, hourID)
		clear(encoded)
		if err != nil {
			return err
		}
	}
	if len(rows) == observationTimelinePageSize {
		if meta.pages == maxObservationTimelinePages {
			return &storage.StoreError{Code: storage.Incomplete}
		}
		meta.pages++
		rows = nil
	}
	rows = append(rows, captureWindowCandidate{id: id, at: at.UTC()})
	meta.count++
	pageID := observationTimelinePageID(hourID, meta.pages-1)
	encoded = encodeObservationTimelinePage(rows)
	defer clear(encoded)
	if err := tx.putIndexValueLocked(captureTimelineNS, pageID, encoded); err != nil {
		return err
	}
	if err := tx.putIndexValueLocked(captureTimelineNS, hourID, encodeObservationTimelineMeta(meta)); err != nil {
		return err
	}
	return tx.putIndexValueLocked(captureTimelineRefNS, refID, []byte(pageID))
}

// Source erasure already visits every authenticated receipt in bounded batches.
// The first receipt in each hour removes that edition's directory row; later
// receipts are idempotent. Suppressed editions are never returned during erasure.
func (tx *Tx) removeCaptureTimelineLocked(id string, at time.Time) error {
	hourID := observationTimelineHourID(at)
	refID := id + ":" + hourID
	ref, found, err := tx.readIndexValueLocked(captureTimelineRefNS, refID, 64)
	if err != nil || !found {
		return err
	}
	pageID := string(ref)
	clear(ref)
	actualHour, _, err := parseObservationTimelinePageID(pageID)
	if err != nil || actualHour != hourID {
		return ErrCorrupt
	}
	encoded, found, err := tx.readIndexValueLocked(captureTimelineNS, pageID, maxObservationTimelinePage)
	if err != nil {
		return err
	}
	if !found {
		return ErrCorrupt
	}
	rows, err := decodeObservationTimelinePage(encoded, hourID)
	clear(encoded)
	if err != nil {
		return err
	}
	index := -1
	for i := range rows {
		if rows[i].id == id {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrCorrupt
	}
	rows = append(rows[:index], rows[index+1:]...)
	encoded, found, err = tx.readIndexValueLocked(captureTimelineNS, hourID, 9)
	if err != nil {
		return err
	}
	if !found {
		return ErrCorrupt
	}
	meta, err := decodeObservationTimelineMeta(encoded)
	clear(encoded)
	if err != nil || meta.count < 1 {
		return ErrCorrupt
	}
	meta.count--
	encoded = encodeObservationTimelinePage(rows)
	defer clear(encoded)
	if err := tx.putIndexValueLocked(captureTimelineNS, pageID, encoded); err != nil {
		return err
	}
	if err := tx.putIndexValueLocked(captureTimelineNS, hourID, encodeObservationTimelineMeta(meta)); err != nil {
		return err
	}
	return tx.deleteIndexValueLocked(captureTimelineRefNS, refID)
}
