package reminders

import (
	"context"
	"slices"
	"sync"
)

// This read-only decision port remains usable while an already committed
// erasure journal fences ordinary source/task reads. It exposes only native
// identifiers to cancel, never task titles or deleted source content.
type meetingNotificationCleanupReader interface {
	CleanupReminderIDs(context.Context, string, []string) ([]string, error)
}

// GuardMeeting holds scheduling through recovery and whole-meeting erasure.
// Unrelated native requests retain their original delivery and payload: a due
// one-shot cannot safely be reconstructed from its durable attempted receipt.
func (s *Service) GuardMeeting(parent context.Context, meetingID string) (func(), error) {
	ctx, done, err := s.enter(parent)
	if err != nil {
		return nil, err
	}
	reader, ok := s.repository.(meetingNotificationCleanupReader)
	if !ok {
		done()
		return nil, ErrUnavailable
	}
	snapshot, err := s.port.Snapshot(ctx)
	if err != nil {
		done()
		return nil, err
	}
	if !validSnapshot(snapshot) {
		done()
		return nil, ErrUnavailable
	}
	ids := slices.Clone(snapshot.Pending)
	for _, id := range snapshot.Delivered {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	remove, err := reader.CleanupReminderIDs(ctx, meetingID, ids)
	if err != nil {
		done()
		return nil, err
	}
	if len(remove) > len(ids) {
		done()
		return nil, ErrUnavailable
	}
	seen := make(map[string]bool, len(remove))
	for _, id := range remove {
		if !slices.Contains(ids, id) || seen[id] {
			done()
			return nil, ErrUnavailable
		}
		seen[id] = true
	}
	for start := 0; start < len(remove); start += 256 {
		if err := s.port.Remove(ctx, remove[start:min(start+256, len(remove))]); err != nil {
			done()
			return nil, err
		}
	}
	var once sync.Once
	return func() { once.Do(func() { done(); s.Wake() }) }, nil
}
