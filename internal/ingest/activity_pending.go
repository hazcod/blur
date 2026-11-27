package ingest

import (
	"context"

	"github.com/hazcod/blur/internal/activity"
)

// The outbox may sit behind the same policy admission adapter as captures.
// Stage success means durable acceptance; final writes acknowledge atomically.
type activityJournal interface {
	StageActivitySegment(context.Context, activity.Segment) error
	PeekPendingActivity(context.Context) (activity.Segment, bool, error)
	DiscardPendingActivity(context.Context, string) error
}

const maxActivityDrain = 32

func (s *Service) activityJournal() activityJournal {
	if journal, ok := s.activitySink.(activityJournal); ok {
		return journal
	}
	journal, _ := s.sink.(activityJournal)
	return journal
}

func (s *Service) removePendingActivityHead() {
	clear(s.pendingActivity[0].SourceIDs)
	s.pendingActivity[0] = activity.Segment{}
	copy(s.pendingActivity, s.pendingActivity[1:])
	s.pendingActivity[len(s.pendingActivity)-1] = activity.Segment{}
	s.pendingActivity = s.pendingActivity[:len(s.pendingActivity)-1]
}

func (s *Service) flushPendingActivity(ctx context.Context) error {
	if s == nil || s.activitySink == nil || ctx == nil {
		return ErrInvalidService
	}
	journal := s.activityJournal()
	if journal != nil {
		// At most one Engine.Apply batch remains in memory in the real capture
		// loop. A staging failure stops before any subsequent Apply or capture.
		for len(s.pendingActivity) > 0 {
			if err := journal.StageActivitySegment(ctx, s.pendingActivity[0]); err != nil {
				if !isPermanentActivityWriteError(err) {
					s.logger.Error("encrypted activity staging failed", "operation", "activity", "result", safeActivityWriteResult(err))
					return err
				}
				s.logger.Error("encrypted activity write rejected", "operation", "activity", "result", safeActivityWriteResult(err), "dropped", 1)
			}
			s.removePendingActivityHead()
		}
		for drained := 0; drained < maxActivityDrain; drained++ {
			segment, found, err := journal.PeekPendingActivity(ctx)
			if err != nil {
				return err
			}
			if !found {
				s.activityReplay = false
				return nil
			}
			if err := s.activitySink.PutActivitySegment(ctx, segment); err != nil {
				if !isPermanentActivityWriteError(err) {
					s.activityReplay = true
					s.logger.Warn("encrypted activity replay deferred", "operation", "activity", "result", safeActivityWriteResult(err))
					return nil // Already accepted durably; retry on the next turn.
				}
				s.logger.Error("encrypted activity write rejected", "operation", "activity", "result", safeActivityWriteResult(err), "dropped", 1)
			}
			if err := journal.DiscardPendingActivity(ctx, segment.ID); err != nil {
				return err
			}
			clear(segment.SourceIDs)
		}
		s.activityReplay = true
		return nil
	}
	// Adapters without an outbox acknowledge synchronously. A retryable write
	// failure keeps this event's output and prevents the engine from advancing.
	for drained := 0; drained < maxActivityDrain && len(s.pendingActivity) > 0; drained++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.activitySink.PutActivitySegment(ctx, s.pendingActivity[0]); err != nil {
			if !isPermanentActivityWriteError(err) {
				s.logger.Error("encrypted activity write failed", "operation", "activity", "result", safeActivityWriteResult(err), "retry_pending", len(s.pendingActivity))
				return err
			}
			s.logger.Error("encrypted activity write rejected", "operation", "activity", "result", safeActivityWriteResult(err), "dropped", 1)
		}
		s.removePendingActivityHead()
	}
	return nil
}

func activityWriteError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrActivityFailed
}
