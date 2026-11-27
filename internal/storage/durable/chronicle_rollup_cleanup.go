package durable

import (
	"encoding/json"
	"slices"
	"sort"

	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const rollupCleanupNS = "chronicle-rollup-cleanup"
const rollupCleanupBatch = 32

// The forward row remains in its catalog until cleanup finishes. Public
// evidence fields are empty while these encrypted reverse obligations remain.
type rollupCleanup struct {
	Version   int
	SourceIDs []string
	Deleting  bool
}

func (s *Store) rollupCleanupReader(reader bolt.Reader, id string) (rollupCleanup, bool, error) {
	var c rollupCleanup
	raw, found, err := s.readIndexValueReader(reader, rollupCleanupNS, id, 20<<10)
	if err != nil || !found {
		return c, found, err
	}
	defer clear(raw)
	if json.Unmarshal(raw, &c) != nil || c.Version != 1 || len(c.SourceIDs) > chronicle.MaxRollupSources || !sort.StringsAreSorted(c.SourceIDs) || len(c.SourceIDs) == 0 && !c.Deleting {
		return c, false, ErrCorrupt
	}
	for i, source := range c.SourceIDs {
		if !validHash(source) || i > 0 && c.SourceIDs[i-1] == source {
			return c, false, ErrCorrupt
		}
	}
	return c, true, nil
}

func (tx *Tx) putRollupCleanup(id string, c rollupCleanup, existed bool) error {
	if len(c.SourceIDs) == 0 && !c.Deleting {
		if existed {
			return tx.deleteKnowledgeRecord(rollupCleanupNS, id)
		}
		return nil
	}
	return tx.PutIndexRecord(rollupCleanupNS, id, mustJSON(c))
}

func rollupSourcesWithCleanup(active, pending []string) []string {
	if len(pending) == 0 {
		return active
	}
	if len(active) == 0 {
		return pending
	}
	all := append(slices.Clone(active), pending...)
	sort.Strings(all)
	return slices.Compact(all)
}

func (tx *Tx) stageRollupCleanup(r *chronicle.Rollup, deleting bool) error {
	c, found, err := tx.store.rollupCleanupReader(tx.writer, r.ID)
	if err != nil {
		return err
	}
	if found && len(r.SourceIDs) != 0 {
		return ErrCorrupt
	}
	if !found {
		c = rollupCleanup{Version: 1, SourceIDs: slices.Clone(r.SourceIDs)}
	}
	if !found || deleting && !c.Deleting {
		c.Deleting = c.Deleting || deleting
		if err := tx.putRollupCleanup(r.ID, c, found); err != nil {
			return err
		}
	}
	old := *r
	clearRollupContent(r)
	r.ChildIDs, r.SourceIDs = nil, nil
	r.Dirty, r.Revision = true, r.Revision+1
	return tx.writeRollup(*r, &old)
}

// Cleanup never discards the receipt before its reverse edges are unlinked.
// A final hard deletion removes receipt, catalog and forward row atomically.
func (tx *Tx) drainRollupCleanup(r *chronicle.Rollup) (pending, deleted bool, err error) {
	c, found, err := tx.store.rollupCleanupReader(tx.writer, r.ID)
	if err != nil || !found {
		return false, false, err
	}
	if r.Text != "" || len(r.SourceIDs) != 0 || len(r.ChildIDs) != 0 || !r.Dirty {
		return false, false, ErrCorrupt
	}
	n := min(rollupCleanupBatch, len(c.SourceIDs))
	for _, source := range c.SourceIDs[:n] {
		removed, err := tx.unlinkRollupSource(source, r.ID)
		if err != nil {
			return false, false, err
		}
		if !removed {
			return false, false, ErrCorrupt
		}
	}
	c.SourceIDs = slices.Clone(c.SourceIDs[n:])
	if len(c.SourceIDs) == 0 && c.Deleting {
		if err := tx.deleteKnowledgeRecord(rollupCleanupNS, r.ID); err != nil {
			return false, false, err
		}
		return false, true, tx.finishDeleteRollup(r.ID)
	}
	if err := tx.putRollupCleanup(r.ID, c, true); err != nil {
		return false, false, err
	}
	old := *r
	r.Revision++
	if err := tx.writeRollup(*r, &old); err != nil {
		return false, false, err
	}
	return len(c.SourceIDs) != 0, false, nil
}

func (tx *Tx) finishDeleteRollup(id string) error {
	if err := tx.removeRollupShardEntry(id); err != nil {
		return err
	}
	if err := tx.syncRollupWork(id, false); err != nil {
		return err
	}
	return tx.deleteKnowledgeRecord(chronicleRollupNS, id)
}

func (tx *Tx) deleteRollup(id string) error {
	r, found, err := tx.readRollup(id)
	if err != nil || !found {
		return err
	}
	c, pending, err := tx.store.rollupCleanupReader(tx.writer, id)
	if err != nil {
		return err
	}
	if !pending && len(r.SourceIDs) == 0 {
		return tx.finishDeleteRollup(id)
	}
	if !pending || !c.Deleting {
		if err := tx.stageRollupCleanup(&r, true); err != nil {
			return err
		}
	}
	_, _, err = tx.drainRollupCleanup(&r)
	return err
}
