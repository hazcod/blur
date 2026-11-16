package durable

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const (
	deletionCutoffNS      = "deletion-cutoffs-v1"
	deletionChangeNS      = "deletion-changes-v1"
	deletionSequenceID    = "sequence"
	deletionMigrationID   = "deletion-cutoffs-v1"
	deletionChangeRows    = 64
	maxDeletionPageBytes  = 1 + deletionChangeRows*tombstoneEntryBytes
	MaxDeletionChangePage = 256
	maxDeletionSequence   = 1<<48 - 1
)

// ErrSourceDeleted is a rejected stale write, not a storage failure. A later
// real capture remains admissible under the original cutoff semantics.
var ErrSourceDeleted error = &classifiedStoreError{code: "deleted", message: "durable store: source was deleted"}

type importedObservationKey struct{}

type DeletionChange struct {
	Sequence uint64
	Tombstone
}

type DeletionChangePage struct {
	Changes []DeletionChange
	Next    uint64
	HasMore bool
}

func encodeDeletionNumber(n uint64) []byte {
	b := make([]byte, 9)
	b[0] = 1
	binary.BigEndian.PutUint64(b[1:], n)
	return b
}

func decodeDeletionNumber(b []byte) (uint64, error) {
	if len(b) != 9 || b[0] != 1 {
		return 0, ErrCorrupt
	}
	return binary.BigEndian.Uint64(b[1:]), nil
}

func decodeDeletionTime(b []byte) (time.Time, error) {
	n, err := decodeDeletionNumber(b)
	at := time.Unix(0, int64(n)).UTC()
	if err != nil || !validCaptureTime(at) {
		return time.Time{}, ErrCorrupt
	}
	return at, nil
}

func (s *Store) deletionCoversReader(reader bolt.Reader, id string, at time.Time) (bool, error) {
	for _, key := range []string{id, AllSourcesTombstone} {
		b, found, err := s.readIndexValueReader(reader, deletionCutoffNS, key, 9)
		if err != nil {
			return false, err
		}
		if !found {
			continue
		}
		cutoff, err := decodeDeletionTime(b)
		clear(b)
		if err != nil {
			return false, err
		}
		if key == AllSourcesTombstone && at.Before(cutoff) || key == id && !at.After(cutoff) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) ObservationDeletionCovers(ctx context.Context, id string, at time.Time) (bool, error) {
	if ctx == nil || !validHash(id) || !validCaptureTime(at) {
		return false, &storage.StoreError{Code: storage.Invalid}
	}
	var covered bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		covered, err = s.deletionCoversReader(reader, id, at)
		return err
	})
	return covered, err
}

func (tx *Tx) checkSourceAdmissionLocked(id string, at time.Time) error {
	covered, err := tx.store.deletionCoversReader(tx.writer, id, at)
	if err != nil {
		return err
	}
	if covered {
		return ErrSourceDeleted
	}
	return nil
}

func deletionChangePageID(index uint64) string { return fmt.Sprintf("page:%012d", index) }

func decodeDeletionPage(b []byte) ([]Tombstone, error) {
	if len(b) < 1 || b[0] != 1 || len(b) > maxDeletionPageBytes {
		return nil, ErrCorrupt
	}
	items, err := decodeTombstones(b[1:])
	if err != nil || len(items) == 0 || len(items) > deletionChangeRows {
		return nil, ErrCorrupt
	}
	return items, nil
}

func (tx *Tx) putDeletionCutoffLocked(t Tombstone) error {
	b, found, err := tx.readIndexValueLocked(deletionCutoffNS, t.SourceID, 9)
	if err != nil {
		return err
	}
	if found {
		previous, err := decodeDeletionTime(b)
		clear(b)
		if err != nil {
			return err
		}
		if !t.At.After(previous) {
			return nil
		}
	}
	b, found, err = tx.readIndexValueLocked(deletionCutoffNS, deletionSequenceID, 9)
	if err != nil || !found {
		return ErrCorrupt
	}
	seq, err := decodeDeletionNumber(b)
	clear(b)
	if err != nil || seq >= maxDeletionSequence {
		return ErrCorrupt
	}
	pageID := deletionChangePageID(seq / deletionChangeRows)
	b, found, err = tx.readIndexValueLocked(deletionChangeNS, pageID, maxDeletionPageBytes)
	if err != nil {
		return err
	}
	var page []Tombstone
	if found {
		page, err = decodeDeletionPage(b)
		clear(b)
		if err != nil {
			return err
		}
	}
	if len(page) != int(seq%deletionChangeRows) {
		return ErrCorrupt
	}
	page = append(page, Tombstone{SourceID: t.SourceID, At: t.At.UTC()})
	encoded := append([]byte{1}, encodeTombstones(page)...)
	defer clear(encoded)
	if err := tx.putIndexValueLocked(deletionChangeNS, pageID, encoded); err != nil {
		return err
	}
	if err := tx.putIndexValueLocked(deletionCutoffNS, t.SourceID, encodeDeletionNumber(uint64(t.At.UTC().UnixNano()))); err != nil {
		return err
	}
	return tx.putIndexValueLocked(deletionCutoffNS, deletionSequenceID, encodeDeletionNumber(seq+1))
}

// DeletionChanges pages committed local arrival order, not deletion time.
// Relayed older timestamps and equal-time boundaries therefore remain visible.
func (s *Store) DeletionChanges(ctx context.Context, after uint64, limit int) (DeletionChangePage, error) {
	if ctx == nil || after > maxDeletionSequence || limit < 1 || limit > MaxDeletionChangePage {
		return DeletionChangePage{}, &storage.StoreError{Code: storage.Invalid}
	}
	result := DeletionChangePage{Next: after}
	err := s.view(ctx, func(reader bolt.Reader) error {
		b, found, err := s.readIndexValueReader(reader, deletionCutoffNS, deletionSequenceID, 9)
		if err != nil || !found {
			return ErrCorrupt
		}
		last, err := decodeDeletionNumber(b)
		clear(b)
		if err != nil || last > maxDeletionSequence || after > last {
			return ErrCorrupt
		}
		var currentPage uint64 = maxDeletionSequence
		var page []Tombstone
		for seq := after + 1; seq <= last && len(result.Changes) < limit; seq++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			index := (seq - 1) / deletionChangeRows
			if index != currentPage {
				b, found, err := s.readIndexValueReader(reader, deletionChangeNS, deletionChangePageID(index), maxDeletionPageBytes)
				if err != nil || !found {
					return ErrCorrupt
				}
				page, err = decodeDeletionPage(b)
				clear(b)
				if err != nil || len(page) != min(deletionChangeRows, int(last-index*deletionChangeRows)) {
					return ErrCorrupt
				}
				currentPage = index
			}
			result.Changes = append(result.Changes, DeletionChange{Sequence: seq, Tombstone: page[(seq-1)%deletionChangeRows]})
			result.Next = seq
		}
		result.HasMore = result.Next < last
		return nil
	})
	if err != nil {
		return DeletionChangePage{}, err
	}
	return result, nil
}

// ImportObservation is the mandatory sync admission path. A stale deletion
// outcome is a skipped record; note failures do not fall back to raw writes.
func (s *Store) ImportObservation(ctx context.Context, o capture.SanitizedObservation, at time.Time) (bool, error) {
	if ctx == nil {
		return false, &storage.StoreError{Code: storage.Invalid}
	}
	ctx = context.WithValue(ctx, importedObservationKey{}, true)
	var err error
	if o.AppID() == "sh.blur.notes" && o.Domain() == "local" {
		err = s.SaveNote(ctx, o, at)
	} else {
		err = s.PutObservationAt(ctx, o, at)
	}
	if errors.Is(err, ErrSourceDeleted) {
		return false, nil
	}
	var storeErr *storage.StoreError
	if errors.As(err, &storeErr) && storeErr.Code == storage.Duplicate {
		return false, nil
	}
	return err == nil, err
}
