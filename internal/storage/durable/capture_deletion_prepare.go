package durable

import (
	"context"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/tasks"
)

// DeleteSourceRecordWithPrepare prepares all native requests before committing
// an unconditional resumable erasure. A captured excerpt resolves to its whole
// edition; this operation deliberately has no wall-clock cutoff.
func (s *Store) DeleteSourceRecordWithPrepare(ctx context.Context, id string, prepare func(string, []tasks.Item) error) error {
	_, err := s.deleteSourceStagedPreparedIf(ctx, id, nil, func(bolt.Reader) (bool, error) { return true, nil }, prepare)
	return err
}

// The caller holds the exclusive state/deletion gate and native scheduling
// guard throughout every read, callback and the subsequent journal commit.
// Only one source's bounded task snapshot is retained across transactions.
func (s *Store) prepareSourceErasureLocked(ctx context.Context, id string, prepare func(string, []tasks.Item) error) error {
	if prepare == nil {
		return nil
	}
	one := func(sourceID string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var items []tasks.Item
		if err := s.captureErasureViewLocked(ctx, func(reader bolt.Reader) error {
			ids, err := s.taskSourceReader(reader, sourceID)
			if err != nil {
				return err
			}
			items = make([]tasks.Item, 0, len(ids))
			for _, taskID := range ids {
				item, found, err := s.taskReader(reader, taskID)
				if err != nil {
					return err
				}
				if !found || !item.References(sourceID) {
					return ErrCorrupt
				}
				items = append(items, item)
			}
			return nil
		}); err != nil {
			return err
		}
		return prepare(sourceID, items)
	}
	if err := one(id); err != nil {
		return err
	}
	var publication string
	var parts int64
	err := s.captureErasureViewLocked(ctx, func(reader bolt.Reader) error {
		edition, summary, found, err := s.captureEditionReader(reader, id)
		if found && err == nil {
			publication, parts = edition.Publication, summary.PartCount()
		}
		return err
	})
	if err != nil {
		return err
	}
	for index := int64(0); index < parts; index++ {
		var head string
		if err := s.captureErasureViewLocked(ctx, func(reader bolt.Reader) error {
			var err error
			head, err = s.captureRangeHead(reader, id, publication, index)
			return err
		}); err != nil {
			return err
		}
		next := func(sourceID string) (string, error) {
			if sourceID == "" {
				return "", nil
			}
			var following string
			err := s.captureErasureViewLocked(ctx, func(reader bolt.Reader) error {
				mapping, found, err := s.captureIndexPartReader(reader, sourceID)
				if err != nil {
					return err
				}
				if !found || !mapping.Range || mapping.EditionID != id || mapping.Publication != publication || mapping.Index != index {
					return ErrCorrupt
				}
				following = mapping.Next
				return nil
			})
			return following, err
		}
		fast := head
		for head != "" {
			following, err := next(head)
			if err != nil {
				return err
			}
			if err := one(head); err != nil {
				return err
			}
			head = following
			// Constant-memory cycle detection keeps malformed authenticated
			// links from becoming an unbounded sequence of small transactions.
			for step := 0; step < 2 && fast != ""; step++ {
				fast, err = next(fast)
				if err != nil {
					return err
				}
			}
			if head != "" && head == fast {
				return ErrCorrupt
			}
		}
		if err := one(capturePartSourceID(id, publication, index)); err != nil {
			return err
		}
	}
	return nil
}
