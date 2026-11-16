package client

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

// mcpSightingFieldsBytes is the response budget reserved per capture for the
// sighting_count and last_seen_at fields.
const mcpSightingFieldsBytes = 64

// mcpSighting returns how often a capture's content was seen and when it was
// last seen. Stores that do not track sightings report a single sighting at
// the capture time (zero when that time is unknown).
//
// Only damaged sighting metadata (a corrupt record, or a chain too long to
// count reliably) degrades: it returns count 0 and an empty time, the fields
// are omitted for that item and the caller reports it in sighting_unavailable.
// Every other storage error propagates so a broken store is never masked.
func (c *Client) mcpSighting(ctx context.Context, id string, captured time.Time) (int64, string, error) {
	if reader, ok := c.data.(storage.ObservationSightingStatsReader); ok {
		count, last, found, err := reader.ObservationSightingStats(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return 0, "", ctx.Err()
			}
			if errors.Is(err, durable.ErrCorrupt) || errors.Is(err, durable.ErrSightingUnknown) {
				return 0, "", nil
			}
			return 0, "", err
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

// noteSightingUnavailable logs once per response, without identifiers, that
// some captures omitted their sighting fields.
func noteSightingUnavailable(unavailable int) {
	if unavailable > 0 {
		slog.Debug("sighting metadata unavailable for some captures", "operation", "mcp_read", "count", unavailable)
	}
}
