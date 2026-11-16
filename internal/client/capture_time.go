package client

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/storage"
)

// A source's retention baseline must never be presented as an observed visit.
// Backends without time metadata continue to return undated evidence.
func (c *Client) sourceCaptureTime(ctx context.Context, id string, at time.Time) (time.Time, bool, error) {
	if at.IsZero() {
		if reader, ok := c.data.(storage.SourceEvidenceTimeReader); ok {
			source, capturedAt, found, err := reader.GetSourceEvidenceWithCaptureTime(ctx, id)
			if err != nil {
				return time.Time{}, false, err
			}
			if !found || source.Validate() != nil || source.SourceID() != id {
				return time.Time{}, false, ErrMemoryChanging
			}
			at = capturedAt
		} else if reader, ok := c.data.(interface {
			ObservationCaptureTime(context.Context, string) (time.Time, error)
		}); ok {
			var err error
			at, err = reader.ObservationCaptureTime(ctx, id)
			if err != nil {
				return time.Time{}, false, err
			}
		}
	}
	if classifier, ok := c.data.(interface{ IsObservationTimeKnown(time.Time) bool }); ok && !at.IsZero() && !classifier.IsObservationTimeKnown(at) {
		return time.Time{}, true, nil
	}
	return at, false, nil
}
