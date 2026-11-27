package client

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/storage"
)

type librarySourceSightings interface {
	SourceEvidenceSightingAt(context.Context, string, time.Time, time.Time) (time.Time, bool, error)
}

// Scope each claim by its own immutable support. A recent mention of the same
// entity does not date its older claims, and undated authored text cannot prove
// that something was observed in a requested period. Returned slices are private
// projections; filtering never changes the stored Library or its erasure edges.
func (c *Client) datedLibraryEntities(ctx context.Context, plan assistant.Plan, entities []knowledge.Entity) ([]knowledge.Entity, error) {
	if plan.Scope.IsZero() {
		return entities, nil
	}
	start, end, _, _ := c.planWindow(plan)
	known := func(at time.Time) bool {
		if at.IsZero() {
			return false
		}
		if classifier, ok := c.data.(interface{ IsObservationTimeKnown(time.Time) bool }); ok {
			return classifier.IsObservationTimeKnown(at)
		}
		return true
	}
	cache := map[string]bool{}
	observed := func(id string) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if found, ok := cache[id]; ok {
			return found, nil
		}
		if reader, ok := c.data.(storage.SourceEvidenceTimeReader); ok {
			source, at, found, err := reader.GetSourceEvidenceWithCaptureTime(ctx, id)
			if err != nil {
				return false, err
			}
			if !found {
				cache[id] = false
				return false, nil
			}
			if source.Validate() != nil || source.SourceID() != id {
				return false, ErrInvalidRequest
			}
			if known(at) && !at.Before(start) && at.Before(end) {
				cache[id] = true
				return true, nil
			}
			// An outside or unknown timestamp cannot prove absence: the same
			// immutable source can have another exact visit inside the period.
		}
		var lookup func(context.Context, string, time.Time, time.Time) (time.Time, bool, error)
		if reader, ok := c.data.(librarySourceSightings); ok {
			lookup = reader.SourceEvidenceSightingAt
		} else if reader, ok := c.data.(storage.ObservationSightings); ok {
			lookup = reader.ObservationSightingAt
		}
		if lookup != nil {
			// Physical visit reads accept a month at a time. Walk lazily until
			// supported, without imposing a lifetime/history horizon.
			for finish := end; finish.After(start); {
				begin := finish.Add(-storage.MaxObservationWindow)
				if begin.Before(start) {
					begin = start
				}
				at, found, err := lookup(ctx, id, begin, finish)
				if err != nil {
					return false, err
				}
				if found {
					if at.Before(begin) || !at.Before(finish) {
						return false, ErrInvalidRequest
					}
					if !known(at) {
						finish = at
						continue
					}
					cache[id] = true
					return true, nil
				}
				finish = begin
			}
			cache[id] = false
			return false, nil
		}
		at, unknown, err := c.sourceCaptureTime(ctx, id, time.Time{})
		if err != nil {
			return false, err
		}
		found := !unknown && !at.IsZero() && !at.Before(start) && at.Before(end)
		cache[id] = found
		return found, nil
	}
	supported := func(sources []knowledge.Source, supersedes []string) (bool, error) {
		for _, source := range sources {
			if source.ID != "" {
				found, err := observed(source.ID)
				if err != nil || found {
					return found, err
				}
			}
			if source.UserAuthored {
				// A correction's support is the exact captures it corrects;
				// its authoring time is not inferred from another entity fact.
				for _, id := range supersedes {
					found, err := observed(id)
					if err != nil || found {
						return found, err
					}
				}
			}
		}
		return false, nil
	}
	result := make([]knowledge.Entity, 0, len(entities))
	for _, entity := range entities {
		filtered := entity
		filtered.Facts, filtered.OpenLoops, filtered.ActivitySourceIDs = nil, nil, nil
		for _, fact := range entity.Facts {
			if fact.State == knowledge.Dismissed {
				continue
			}
			found, err := supported(fact.Sources, fact.Supersedes)
			if err != nil {
				return nil, err
			}
			if found {
				filtered.Facts = append(filtered.Facts, fact)
			}
		}
		for _, loop := range entity.OpenLoops {
			if loop.State == knowledge.Dismissed {
				continue
			}
			found, err := supported(loop.Sources, nil)
			if err != nil {
				return nil, err
			}
			if found {
				filtered.OpenLoops = append(filtered.OpenLoops, loop)
			}
		}
		if len(filtered.Facts)+len(filtered.OpenLoops) == 0 {
			continue
		}
		for _, id := range entity.ActivitySourceIDs {
			found, err := observed(id)
			if err != nil {
				return nil, err
			}
			if found {
				filtered.ActivitySourceIDs = append(filtered.ActivitySourceIDs, id)
			}
		}
		result = append(result, filtered)
	}
	return result, nil
}
