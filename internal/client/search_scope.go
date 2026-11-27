package client

import (
	"context"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/semantic"
)

// A window bounds a storage transaction, never the complete query horizon.
const searchWindowSpan = 28 * 24 * time.Hour

type searchScopeWindow struct {
	start, end time.Time
	ids        []string
	visits     map[string]time.Time
}

// Window descriptors and activity membership are generated only as consumed.
// A search retains its ranked prefix, not every source ID in its date range.
type searchScopeWalk func(func(searchScopeWindow, error) bool)

func singleSearchWindow(window searchScopeWindow) searchScopeWalk {
	return func(yield func(searchScopeWindow, error) bool) { yield(window, nil) }
}

// Meaning rewrites use the same window membership as the exact Search pass.
func (c *Client) recallSearchWindows(ctx context.Context, scope semantic.Query) (searchScopeWalk, error) {
	if scope.Start.IsZero() || scope.End.Sub(scope.Start) <= searchWindowSpan && len(scope.ScopeSourceIDs) > 0 {
		return singleSearchWindow(searchScopeWindow{start: scope.Start, end: scope.End, ids: scope.ScopeSourceIDs}), nil
	}
	return c.searchScopeWindows(ctx, assistant.Plan{Scope: assistant.Scope{Start: scope.Start, End: scope.End}}, true, nil)
}

func splitSearchWindows(start, end time.Time) (searchScopeWalk, error) {
	if !end.After(start) {
		return nil, ErrActivityLimit
	}
	return func(yield func(searchScopeWindow, error) bool) {
		for to := end; to.After(start); {
			from := to.Add(-searchWindowSpan)
			if from.Before(start) {
				from = start
			}
			if !yield(searchScopeWindow{start: from, end: to}, nil) {
				return
			}
			to = from
		}
	}, nil
}

func (c *Client) searchScopeWindows(ctx context.Context, plan assistant.Plan, bounded bool, segments []activity.Segment) (searchScopeWalk, error) {
	if ctx == nil {
		return nil, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !bounded {
		return singleSearchWindow(searchScopeWindow{}), nil
	}
	start, end, _, _ := c.planWindow(plan)
	windows, err := splitSearchWindows(start, end)
	if err != nil {
		return nil, err
	}
	return func(yield func(searchScopeWindow, error) bool) {
		for window, err := range windows {
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				yield(searchScopeWindow{}, err)
				return
			}
			// Capture/sighting dates are authoritative independently of foreground
			// metadata. Activity pages additionally admit legacy revisited sources.
			if !yield(window, nil) {
				return
			}
			stopped := false
			err = c.walkSearchActivity(ctx, window, segments, func(rows []activity.Segment) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				visits := make(map[string]time.Time)
				for _, row := range rows {
					if row.Validate() != nil {
						return ErrInvalidRequest
					}
					if !row.WallEnd.After(window.start) || !row.WallStart.Before(window.end) {
						continue
					}
					at := row.WallStart
					// Keep the actual visit across internal child boundaries. Only the
					// original requested boundary can clip a cross-boundary visit's time.
					if at.Before(start) {
						at = start
					}
					for _, id := range row.SourceIDs {
						if !semantic.ValidID(id) {
							return ErrInvalidRequest
						}
						if at.After(visits[id]) {
							visits[id] = at
						}
					}
				}
				if len(visits) == 0 {
					return nil
				}
				child := searchScopeWindow{start: window.start, end: window.end, visits: visits}
				for id := range visits {
					child.ids = append(child.ids, id)
				}
				sort.Strings(child.ids)
				if !yield(child, nil) {
					stopped = true
					return errSearchWalkStopped
				}
				return nil
			})
			if stopped {
				return
			}
			if err != nil {
				yield(searchScopeWindow{}, err)
				return
			}
		}
	}, nil
}

func (c *Client) searchExactWindows(ctx context.Context, query search.ExactQuery, windows searchScopeWalk) (search.ExactPage, error) {
	var result search.ExactPage
	best := make(map[string]search.IndexHit)
	limit := query.TopK
	if limit == 0 {
		limit = search.MaxResults
	}
	for window, walkErr := range windows {
		if walkErr != nil {
			return search.ExactPage{}, walkErr
		}
		if err := ctx.Err(); err != nil {
			return search.ExactPage{}, err
		}
		child := query
		child.CapturedStart, child.CapturedEnd, child.ScopeSourceIDs = window.start, window.end, window.ids
		page, err := c.data.SearchExact(ctx, child)
		if err != nil {
			return search.ExactPage{}, err
		}
		result.Rows += page.Rows
		result.Nodes += page.Nodes
		result.Bytes += page.Bytes
		result.HasMore = result.HasMore || page.HasMore
		result.IndexingPending = result.IndexingPending || page.IndexingPending
		result.CoverageLimited = result.CoverageLimited || page.CoverageLimited
		for _, hit := range page.Hits {
			if at := window.visits[hit.SourceID]; at.After(hit.CapturedAt) {
				hit.CapturedAt = at
			}
			previous, found := best[hit.SourceID]
			if !found || hit.Score > previous.Score {
				if previous.CapturedAt.After(hit.CapturedAt) {
					hit.CapturedAt = previous.CapturedAt
				}
				best[hit.SourceID] = hit
				continue
			}
			if hit.CapturedAt.After(previous.CapturedAt) {
				previous.CapturedAt = hit.CapturedAt
				best[hit.SourceID] = previous
			}
		}
		result.Hits = result.Hits[:0]
		for _, hit := range best {
			result.Hits = append(result.Hits, hit)
		}
		sort.Slice(result.Hits, func(i, j int) bool {
			if result.Hits[i].Score != result.Hits[j].Score {
				return result.Hits[i].Score > result.Hits[j].Score
			}
			return result.Hits[i].SourceID < result.Hits[j].SourceID
		})
		if len(result.Hits) > limit {
			result.HasMore = true
			for _, hit := range result.Hits[limit:] {
				delete(best, hit.SourceID)
			}
			result.Hits = result.Hits[:limit]
		}
	}
	if err := ctx.Err(); err != nil {
		return search.ExactPage{}, err
	}
	return result, nil
}
