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
	_, err := s.deleteSourceStagedIf(ctx, id, nil, func(reader bolt.Reader) (bool, error) {
		resolved, _, err := s.resolveCaptureSourceReader(reader, id)
		if err != nil {
			return false, err
		}
		if err := s.prepareSourceErasureReader(ctx, reader, resolved, prepare); err != nil {
			return false, err
		}
		return true, nil
	})
	return err
}
func (s *Store) prepareSourceErasureReader(ctx context.Context, reader bolt.Reader, id string, prepare func(string, []tasks.Item) error) error {
	if prepare == nil {
		return nil
	}
	one := func(sourceID string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		ids, err := s.taskSourceReader(reader, sourceID)
		if err != nil {
			return err
		}
		items := make([]tasks.Item, 0, len(ids))
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
		return prepare(sourceID, items)
	}
	if err := one(id); err != nil {
		return err
	}
	edition, summary, found, err := s.captureEditionReader(reader, id)
	if err != nil || !found {
		return err
	}
	// Only one source's bounded task snapshot is allocated at a time. The gate
	// remains held across preparation and journal admission; no part can change
	// or publish a new task between its native cleanup and durable deletion.
	for index := int64(0); index < summary.PartCount(); index++ {
		if err := one(capturePartSourceID(id, edition.Publication, index)); err != nil {
			return err
		}
	}
	return nil
}
