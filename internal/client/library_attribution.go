package client

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/knowledge"
)

// Resolve optional display metadata during the existing background Library
// read. Rendering never opens sources, and missing/legacy times stay unknown.
func (c *Client) libraryAttribution(ctx context.Context, entity knowledge.Entity) knowledge.Entity {
	entities := []knowledge.Entity{entity}
	c.libraryAttributions(ctx, entities)
	return entities[0]
}

func (c *Client) libraryAttributions(ctx context.Context, entities []knowledge.Entity) {
	// Several subjects can come from the same passage. Keep this optional
	// metadata bounded and reuse each read for the current visible page only.
	times := map[string]time.Time{}
	sourceTime := func(id string) time.Time {
		if at, found := times[id]; found {
			return at
		}
		if len(times) >= knowledge.MaxPageSize || ctx.Err() != nil {
			return time.Time{}
		}
		at, unknown, err := c.sourceCaptureTime(ctx, id, time.Time{})
		if err != nil || unknown {
			at = time.Time{}
		}
		times[id] = at
		return at
	}
	for i, entity := range entities {
		entities[i] = libraryEntityAttribution(entity, sourceTime)
	}
}

func libraryEntityAttribution(entity knowledge.Entity, sourceTime func(string) time.Time) knowledge.Entity {
	if entity.Inference == nil || entity.Inference.AppID != "sh.blur.meetings" || !entity.Inference.SourceTime.IsZero() {
		return entity
	}
	inference := *entity.Inference
	entity.Inference = &inference
	seen := map[string]bool{}
	read := func(id string) bool {
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
		if at := sourceTime(id); !at.IsZero() {
			inference.SourceTime = at
			return true
		}
		return len(seen) >= 8
	}
	for _, id := range entity.ActivitySourceIDs {
		if read(id) {
			return entity
		}
	}
	for _, fact := range entity.Facts {
		for _, source := range fact.Sources {
			if read(source.ID) {
				return entity
			}
		}
	}
	return entity
}
