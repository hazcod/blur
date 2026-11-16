package durable

import (
	"context"
	"encoding/binary"

	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

type deletionMigration struct {
	next, count int
	complete    bool
}

func encodeDeletionMigration(m deletionMigration) []byte {
	b := make([]byte, 10)
	b[0] = 1
	if m.complete {
		b[1] = 1
	}
	binary.BigEndian.PutUint32(b[2:6], uint32(m.next))
	binary.BigEndian.PutUint32(b[6:10], uint32(m.count))
	return b
}

func decodeDeletionMigration(b []byte) (deletionMigration, error) {
	if len(b) != 10 || b[0] != 1 || b[1] > 1 {
		return deletionMigration{}, ErrCorrupt
	}
	m := deletionMigration{next: int(binary.BigEndian.Uint32(b[2:6])), count: int(binary.BigEndian.Uint32(b[6:10])), complete: b[1] == 1}
	if m.count > MaxTombstones || m.next > m.count || m.complete != (m.next == m.count) {
		return deletionMigration{}, ErrCorrupt
	}
	return m, nil
}

// Run only while startup owns the unexposed store. Every batch and progress
// checkpoint is atomic. A crash leaves the current schema installed, so older writers
// cannot enter while this bounded migration is incomplete.
func (s *Store) ensureDeletionCutoffs(ctx context.Context, afterBatch func(int) error) error {
	b, found, err := s.readIndexValue(ctx, captureTimeMigrationNamespace, deletionMigrationID, 10)
	if err != nil {
		return err
	}
	var m deletionMigration
	if found {
		m, err = decodeDeletionMigration(b)
		clear(b)
		if err != nil {
			return err
		}
		if m.complete {
			return s.validateDeletionCutoffTail(ctx)
		}
	}
	legacyBytes, exists, err := s.readIndexValue(ctx, tombstoneNS, tombstoneLogID, MaxTombstones*tombstoneEntryBytes+1)
	if err != nil {
		return err
	}
	var legacy []Tombstone
	if exists {
		legacy, err = decodeTombstones(legacyBytes)
		clear(legacyBytes)
		if err != nil {
			return err
		}
	}
	if found && m.count != len(legacy) {
		return ErrCorrupt
	}
	if !found {
		m.count, m.complete = len(legacy), len(legacy) == 0
		if err := s.Update(ctx, func(tx *Tx) error {
			tx.mu.Lock()
			defer tx.mu.Unlock()
			b, exists, err := tx.readIndexValueLocked(deletionCutoffNS, deletionSequenceID, 9)
			clear(b)
			if err != nil || exists {
				return ErrCorrupt
			}
			if err := tx.putIndexValueLocked(deletionCutoffNS, deletionSequenceID, encodeDeletionNumber(0)); err != nil {
				return err
			}
			return tx.putIndexValueLocked(captureTimeMigrationNamespace, deletionMigrationID, encodeDeletionMigration(m))
		}); err != nil {
			return err
		}
		if afterBatch != nil {
			if err := afterBatch(m.next); err != nil {
				return err
			}
		}
	}
	for !m.complete {
		end := min(m.count, m.next+32)
		next := deletionMigration{next: end, count: m.count, complete: end == m.count}
		if err := s.Update(ctx, func(tx *Tx) error {
			tx.mu.Lock()
			defer tx.mu.Unlock()
			for _, tomb := range legacy[m.next:end] {
				if err := tx.putDeletionCutoffLocked(tomb); err != nil {
					return err
				}
			}
			return tx.putIndexValueLocked(captureTimeMigrationNamespace, deletionMigrationID, encodeDeletionMigration(next))
		}); err != nil {
			return err
		}
		m = next
		if afterBatch != nil {
			if err := afterBatch(m.next); err != nil {
				return err
			}
		}
	}
	return s.validateDeletionCutoffTail(ctx)
}

// The completion marker does not stand in for the cursor and its final page.
// Bound startup work to these two records; page reads validate earlier pages
// as needed and normal admission authenticates each individual cutoff.
func (s *Store) validateDeletionCutoffTail(ctx context.Context) error {
	return s.view(ctx, func(reader bolt.Reader) error {
		b, found, err := s.readIndexValueReader(reader, deletionCutoffNS, deletionSequenceID, 9)
		if err != nil || !found {
			return ErrCorrupt
		}
		last, err := decodeDeletionNumber(b)
		clear(b)
		if err != nil || last > maxDeletionSequence {
			return ErrCorrupt
		}
		if last == 0 {
			return nil
		}
		b, found, err = s.readIndexValueReader(reader, deletionChangeNS, deletionChangePageID((last-1)/deletionChangeRows), maxDeletionPageBytes)
		if err != nil || !found {
			return ErrCorrupt
		}
		page, err := decodeDeletionPage(b)
		clear(b)
		if err != nil || len(page) != int((last-1)%deletionChangeRows)+1 {
			return ErrCorrupt
		}
		return nil
	})
}
