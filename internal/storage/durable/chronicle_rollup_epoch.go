package durable

import (
	"context"
	"encoding/json"
	"math"

	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// This additive encrypted receipt survives erasure of the derived records.
// Revisions alone cannot distinguish a removed period from its recreation.
func (s *Store) rollupEpochReader(reader bolt.Reader) (uint64, error) {
	raw, found, err := s.readIndexValueReader(reader, "chronicle-rollup-meta", "epoch", 32)
	if err != nil || !found {
		return 0, err
	}
	defer clear(raw)
	var epoch uint64
	if json.Unmarshal(raw, &epoch) != nil {
		return 0, ErrCorrupt
	}
	return epoch, nil
}

func (s *Store) advanceRollupEpoch(ctx context.Context) error {
	return s.Update(ctx, func(tx *Tx) error {
		return tx.advanceRollupEpoch()
	})
}

func (tx *Tx) advanceRollupEpoch() error {
	epoch, err := tx.store.rollupEpochReader(tx.writer)
	if err != nil {
		return err
	}
	if epoch == math.MaxUint64 {
		return ErrCorrupt
	}
	return tx.PutIndexRecord("chronicle-rollup-meta", "epoch", mustJSON(epoch+1))
}
