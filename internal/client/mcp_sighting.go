package client

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/storage"
)

// mcpSightingFieldsBytes is the response budget reserved per capture for the
// sighting_count and last_seen_at fields.
const mcpSightingFieldsBytes = 64

// mcpSighting returns how often a capture's content was seen and when it was
// last seen. Stores that do not track sightings report a single sighting at
// the capture time (zero when that time is unknown). A count of 0 with an
// empty time means the sighting metadata was unreadable and is omitted.
func (c *Client) mcpSighting(ctx context.Context, id string, captured time.Time) (int64, string, error) {
	if reader, ok := c.data.(storage.ObservationSightingStatsReader); ok {
		count, last, found, err := reader.ObservationSightingStats(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return 0, "", ctx.Err()
			}
			// Unreadable sighting metadata must not hide the capture itself:
			// omit the sighting fields for this item.
			return 0, "", nil
		}
		if found {
			return count, last.UTC().Format(time.RFC3339Nano), nil
		}
	}
	if captured.IsZero() {
		return 1, "", nil
	}
	return 1, captured.UTC().Format(time.RFC3339Nano), nil
}
