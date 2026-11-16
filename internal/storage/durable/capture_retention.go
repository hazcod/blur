package durable

import (
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const captureRetentionID = "retention-cutoff"

func (s *Store) captureRetentionReader(r bolt.Reader) (time.Time, bool, error) {
	b, found, err := s.readIndexValueReader(r, captureNS, captureRetentionID, 9)
	if err != nil || !found {
		return time.Time{}, found, err
	}
	defer clear(b)
	cutoff, err := decodeDeletionTime(b)
	return cutoff, true, err
}

func (s *Store) captureRetentionCoversReader(r bolt.Reader, at time.Time) (bool, error) {
	cutoff, found, err := s.captureRetentionReader(r)
	return found && at.Before(cutoff), err
}

func (tx *Tx) checkCaptureAdmissionLocked(h capture.CaptureHeader) error {
	if err := tx.checkSourceAdmissionLocked(h.FamilyID(), h.CapturedAt()); err != nil {
		return err
	}
	covered, err := tx.store.captureRetentionCoversReader(tx.writer, h.CapturedAt())
	if err != nil {
		return err
	}
	if covered {
		return ErrSourceDeleted
	}
	return nil
}

func (tx *Tx) putCaptureRetentionLocked(cutoff time.Time) error {
	previous, found, err := tx.store.captureRetentionReader(tx.writer)
	if err != nil || found && !cutoff.After(previous) {
		return err
	}
	return tx.putIndexValueLocked(captureNS, captureRetentionID, encodeDeletionNumber(uint64(cutoff.UnixNano())))
}
