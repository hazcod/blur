package deletion

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/hazcod/blur/internal/meetings"
)

type meetingDeleter interface {
	GetMeeting(context.Context, string) (meetings.Meeting, bool, error)
	MeetingRecords(context.Context, string, meetings.PageRequest) (meetings.RecordPage, error)
	DeleteMeeting(context.Context, string) error
}

// DeleteMeeting prepares the same native-notification, sync and volatile-memory
// cleanup as individual source deletion, then retains the store's restartable
// whole-meeting erasure journal. The Client must fence recording writes first.
func (s *Service) DeleteMeeting(ctx context.Context, id string) error {
	if s == nil || s.store == nil || ctx == nil || !validMeetingDeletionID(id) {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store, ok := s.store.(meetingDeleter)
	if !ok {
		return ErrUnavailable
	}
	// Hold scheduling across the entire multi-source journal. On restart a
	// committed journal may already have removed task reverse links, so clearing
	// pending notices before recovery also covers that case. Release wakes the
	// reminder service; it rebuilds only surviving future/open durable tasks.
	if held, _ := ctx.Value(allNotificationGuardKey{}).(bool); !held {
		if cleanup := s.notificationCleanup(); cleanup != nil {
			release, err := cleanup.GuardAll(ctx)
			if err != nil {
				return err
			}
			defer release()
		}
	}
	if err := s.recoverCommittedDeletion(ctx); err != nil {
		return err
	}
	_, found, err := store.GetMeeting(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return store.DeleteMeeting(ctx, id) // Idempotently retain its replay fence.
	}
	if err := s.prepareMeetingSources(ctx, store, id, time.Now().UTC()); err != nil {
		return err
	}
	return store.DeleteMeeting(ctx, id)
}

func (s *Service) prepareMeetingSources(ctx context.Context, store meetingDeleter, id string, cutoff time.Time) error {
	var after uint64
	limits := meetings.DefaultLimits()
	remaining := limits.MaxSegments + limits.MaxNotes
	// Pages contain a bounded amount of plaintext. The store's opaque cursor
	// may advance over previously deleted rows even when a page contains none.
	for pages := 0; pages <= limits.MaxSegments+limits.MaxNotes; pages++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := store.MeetingRecords(ctx, id, meetings.PageRequest{After: after, Limit: meetings.MaxPageRows, MaxBytes: meetings.MaxPageBytes})
		if err != nil {
			return err
		}
		if len(page.Records) > meetings.MaxPageRows || len(page.Records) > remaining || page.Next < after || page.More && page.Next <= after {
			return ErrUnavailable
		}
		previous := after
		for _, row := range page.Records {
			if row.Record.SessionID != id || row.Sequence <= previous || row.Sequence > page.Next {
				return ErrUnavailable
			}
			decoded, err := hex.DecodeString(row.SourceID)
			valid := err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == row.SourceID
			clear(decoded)
			if !valid {
				return ErrUnavailable
			}
			if err := s.recordTombstone(ctx, row.SourceID, cutoff); err != nil {
				return err
			}
			if s.activity != nil {
				if _, err := s.activity.ForgetSource(row.SourceID); err != nil {
					return err
				}
			}
			previous = row.Sequence
		}
		remaining -= len(page.Records)
		if !page.More {
			return nil
		}
		after = page.Next
	}
	return ErrUnavailable
}

func validMeetingDeletionID(id string) bool {
	if len(id) == 0 || len(id) > 64 || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}
