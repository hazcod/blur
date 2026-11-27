package client

import (
	"context"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/semantic"
)

// Selected-model rewriting/ranking remains one global request. Its exact
// reformulations use the same lazy membership walk. The optional older vector
// controller searches bounded child scopes instead of requiring every legacy
// visit ID in one large semantic query.
func (c *Client) hybridScopedSearch(ctx context.Context, question string, exact search.ExactPage, scope semantic.Query, windows searchScopeWalk, limit int, ask bool, minimum ...float32) (search.ExactPage, error) {
	if c.localAI != nil || c.memoryIndex == nil || !c.memoryIndex.Settings().Enabled {
		if ask {
			return c.hybridAskPage(ctx, question, exact, scope, limit, minimum...)
		}
		return c.hybridPage(ctx, question, exact, scope, limit, minimum...)
	}
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return search.ExactPage{}, ErrMemoryChanging
	}
	queryCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	best := make(map[string]semantic.Hit)
	for window, walkErr := range windows {
		if walkErr != nil {
			return search.ExactPage{}, walkErr
		}
		if err := ctx.Err(); err != nil {
			return search.ExactPage{}, err
		}
		child := scope
		child.Start, child.End, child.ScopeSourceIDs = window.start, window.end, window.ids
		hits, err := c.memoryIndex.Search(queryCtx, question, child)
		if ctx.Err() != nil {
			return search.ExactPage{}, ctx.Err()
		}
		if !c.MemoryReadCurrent(epoch) {
			return search.ExactPage{}, ErrMemoryChanging
		}
		if err != nil {
			exact.CoverageLimited = true
			return exact, nil
		}
		for _, hit := range hits {
			if at := window.visits[hit.SourceID]; at.After(hit.CapturedAt) {
				hit.CapturedAt = at
			}
			old, found := best[hit.SourceID]
			if !found || hit.Score > old.Score {
				if old.CapturedAt.After(hit.CapturedAt) {
					hit.CapturedAt = old.CapturedAt
				}
				best[hit.SourceID] = hit
			} else if hit.CapturedAt.After(old.CapturedAt) {
				old.CapturedAt = hit.CapturedAt
				best[hit.SourceID] = old
			}
		}
		if len(best) > scope.Limit {
			ranked := make([]semantic.Hit, 0, len(best))
			for _, hit := range best {
				ranked = append(ranked, hit)
			}
			sort.Slice(ranked, func(i, j int) bool {
				if ranked[i].Score != ranked[j].Score {
					return ranked[i].Score > ranked[j].Score
				}
				return ranked[i].SourceID < ranked[j].SourceID
			})
			for _, hit := range ranked[scope.Limit:] {
				delete(best, hit.SourceID)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return search.ExactPage{}, err
	}
	if !c.MemoryReadCurrent(epoch) {
		return search.ExactPage{}, ErrMemoryChanging
	}
	hits := make([]semantic.Hit, 0, len(best))
	for _, hit := range best {
		hits = append(hits, hit)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].SourceID < hits[j].SourceID
	})
	return fuseSearchPages(exact, hits, limit, minimum...), nil
}
