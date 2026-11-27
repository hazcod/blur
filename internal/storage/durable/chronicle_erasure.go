package durable

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

var errChronicleErasurePending = errors.New("chronicle erasure in progress")

// Recap erasure is independent of the original-source deletion coordinator.
// Its journal gates publication/reads across restart without taking ownership
// of state.deletionPending, whose exclusive source-erasure protocol is separate.
type chronicleErasure struct {
	Version int
	Hours   bool
	Phase   string
}

func (s *Store) chronicleErasureReader(reader bolt.Reader) (chronicleErasure, bool, error) {
	var j chronicleErasure
	raw, found, err := s.readIndexValueReader(reader, "chronicle-rollup-meta", "erasing", 128)
	if err != nil || !found {
		return j, found, err
	}
	defer clear(raw)
	if json.Unmarshal(raw, &j) != nil || j.Version != 1 || j.Phase != "hours" && j.Phase != "rollups" || j.Phase == "hours" && !j.Hours {
		return j, false, ErrCorrupt
	}
	return j, true, nil
}

func (s *Store) beginChronicleErasure(ctx context.Context, hours bool) error {
	return s.Update(ctx, func(tx *Tx) error {
		j, found, err := s.chronicleErasureReader(tx.writer)
		if err != nil {
			return err
		}
		if found && (!hours || j.Hours) {
			return nil
		}
		if err := tx.advanceRollupEpoch(); err != nil {
			return err
		}
		j = chronicleErasure{Version: 1, Hours: hours, Phase: "rollups"}
		if hours {
			j.Phase = "hours"
		}
		return tx.PutIndexRecord("chronicle-rollup-meta", "erasing", mustJSON(j))
	})
}

// One transaction deletes one hour OR one bounded rollup-cleanup quantum.
// Checking the empty catalog and lifting the gate happen in the same writer
// transaction; the ending epoch also fences any work claimed during erasure.
func (tx *Tx) resumeChronicleErasure() (bool, error) {
	j, found, err := tx.store.chronicleErasureReader(tx.writer)
	if err != nil || !found {
		return !found, err
	}
	if j.Phase == "hours" {
		dates, _, err := tx.readChronicleDays()
		if err != nil {
			return false, err
		}
		if len(dates) != 0 {
			ids, found, err := tx.readChronicleDay(dates[0])
			if err != nil {
				return false, err
			}
			if !found || len(ids) == 0 {
				return false, ErrCorrupt
			}
			return false, tx.deleteChronicle(ids[0])
		}
		j.Phase = "rollups"
		return false, tx.PutIndexRecord("chronicle-rollup-meta", "erasing", mustJSON(j))
	}
	shards, err := tx.readRollupShards()
	if err != nil {
		return false, err
	}
	if len(shards) != 0 {
		ids, found, err := tx.readRollupShard(shards[0])
		if err != nil {
			return false, err
		}
		if !found || len(ids) == 0 {
			return false, ErrCorrupt
		}
		return false, tx.deleteRollup(ids[0])
	}
	if err := tx.advanceRollupEpoch(); err != nil {
		return false, err
	}
	if err := tx.deleteKnowledgeRecord("chronicle-rollup-meta", "erasing"); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) eraseChronicles(ctx context.Context, hours bool) error {
	if ctx == nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	if err := s.beginChronicleErasure(ctx, hours); err != nil {
		return err
	}
	for {
		done := false
		err := s.Update(ctx, func(tx *Tx) error { var err error; done, err = tx.resumeChronicleErasure(); return err })
		if err != nil || done {
			return err
		}
	}
}

// ResumeChronicleErasure performs at most one committed erase quantum. The
// model-free local storage service calls it after restart, including when no
// summary worker exists. With no intent it only reads the small journal; it
// never starts historical discovery or creates derived work.
func (s *Store) ResumeChronicleErasure(ctx context.Context) (worked, more bool, err error) {
	var pending bool
	err = s.view(ctx, func(reader bolt.Reader) error {
		_, pending, err = s.chronicleErasureReader(reader)
		return err
	})
	if err != nil || !pending {
		return false, pending, err
	}
	done := false
	err = s.Update(ctx, func(tx *Tx) error {
		var err error
		done, err = tx.resumeChronicleErasure()
		return err
	})
	return err == nil, !done, err
}
