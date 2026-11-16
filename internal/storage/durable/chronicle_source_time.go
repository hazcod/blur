package durable

import (
	"context"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"time"
)

// Saved-first provenance prevents delete/recreate races. Recap membership is
// separately proved by an actual authenticated visit within the requested hour.
func (s *Store) chronicleSourceVisitReader(ctx context.Context, reader bolt.Reader, id string, first, start, end time.Time) (time.Time, bool, error) {
	mapping, part, err := s.captureIndexPartReader(reader, id)
	if err != nil {
		return time.Time{}, false, err
	}
	if part {
		edition, summary, live, err := s.captureLiveReader(reader, mapping.EditionID)
		if err != nil || !live {
			return time.Time{}, false, err
		}
		if mapping.Publication != edition.Publication || mapping.Index >= summary.PartCount() {
			return time.Time{}, false, ErrCorrupt
		}
		var at time.Time
		err = s.captureSightingInWindow(ctx, reader, mapping.EditionID, edition, start, end, &at)
		return at, !at.IsZero(), err
	}
	return s.sightingInWindow(id, first, start, end, func(namespace, key string, maxBytes int) ([]byte, bool, error) {
		return s.readIndexValueReader(reader, namespace, key, maxBytes)
	})
}
