package durable

import (
	"context"

	"github.com/hazcod/blur/internal/capture"
)

// Finish only this source's durable work receipt. A meeting final is searchable
// when Append succeeds, even if a separate capture-index backlog exists.
func (s *Store) indexMeetingObservation(ctx context.Context, o capture.SanitizedObservation) error {
	terms, err := observationSearchTerms(o)
	if err != nil {
		return err
	}
	ordered := sortedTermKeys(terms)
	for batch := 0; batch <= len(ordered)/exactIndexBatchTerms+1; batch++ {
		finished := false
		err = s.Update(ctx, func(tx *Tx) error {
			tx.mu.Lock()
			defer tx.mu.Unlock()
			key := s.pendingSearchKey(o.ContentID())
			defer clear(key)
			receipt, pending, err := tx.searchIndexPlainLocked(key, "search-pending-v1")
			clear(receipt)
			if err != nil {
				return err
			}

			_, live, err := s.getObservationReader(tx.writer, o.ContentID())
			if err != nil {
				return err
			}
			if !live {
				return ErrSourceDeleted
			}
			if !pending {
				finished = true
				return nil
			}
			done, err := tx.observationSearch().indexSearchTermsBatchLocked(o.ContentID(), o.AppID(), o.Domain(), observationSearchBytes(o), terms, ordered)
			if err != nil {
				return err
			}
			if done {
				finished = true
				return tx.deleteEntryLocked(key)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if finished {
			return nil
		}
	}
	return ErrCorrupt
}
