package client

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/search"
)

// technicalSubjectPage validates complete labels before taking the evidence
// window. Component-word distractors can fill the ordinary Ask pool, so this
// path uses the existing larger exact-index prefix without weakening its terms.
func (c *Client) technicalSubjectPage(ctx context.Context, plan assistant.Plan, bounded bool, windowSegs []activity.Segment, limit int) (search.ExactPage, error) {
	if c == nil || c.data == nil || ctx == nil || len(plan.TechnicalSubjects) == 0 || len(plan.TechnicalSubjects) > search.MaxTerms || limit < 1 || limit > MaxSearchResults {
		return search.ExactPage{}, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return search.ExactPage{}, err
	}
	terms, err := search.NormalizeTokens(strings.Join(plan.TechnicalSubjects, " "), search.MaxTerms)
	if err != nil || len(terms) == 0 {
		return search.ExactPage{}, ErrInvalidRequest
	}
	page, err := c.technicalSubjectPageTerms(ctx, plan, bounded, windowSegs, limit, terms)
	if err != nil || len(page.Hits) > 0 {
		return page, err
	}
	// A source that writes "ThreatBot" indexes one token, never the component
	// tokens searched above. One retry with every subject's components joined
	// keeps the cost bounded; the same subject check still gates every hit.
	joined := make([]string, len(plan.TechnicalSubjects))
	for i, subject := range plan.TechnicalSubjects {
		if joined[i] = search.IdentifierJoined(subject); joined[i] == "" {
			joined[i] = subject
		}
	}
	retryTerms, err := search.NormalizeTokens(strings.Join(joined, " "), search.MaxTerms)
	if err != nil || len(retryTerms) == 0 || slices.Equal(retryTerms, terms) {
		return page, nil
	}
	retry, err := c.technicalSubjectPageTerms(ctx, plan, bounded, windowSegs, limit, retryTerms)
	if err != nil {
		return search.ExactPage{}, err
	}
	// Report truncation from either attempt even when only the first found
	// nothing supported.
	retry.HasMore = retry.HasMore || page.HasMore
	retry.CoverageLimited = retry.CoverageLimited || page.CoverageLimited
	retry.IndexingPending = retry.IndexingPending || page.IndexingPending
	return retry, nil
}

func (c *Client) technicalSubjectPageTerms(ctx context.Context, plan assistant.Plan, bounded bool, windowSegs []activity.Segment, limit int, terms []string) (search.ExactPage, error) {
	filters, err := memoryFilters(plan.AppIDs, plan.Domains)
	if err != nil {
		return search.ExactPage{}, err
	}
	if bounded {
		start, end, _, _ := c.planWindow(plan)
		if end.Sub(start) > searchWindowSpan {
			windowSegs = nil
		}
	}
	windows, err := c.searchScopeWindows(ctx, plan, bounded, windowSegs)
	if err != nil {
		return search.ExactPage{}, err
	}
	best := make(map[string]search.IndexHit)
	var merged search.ExactPage
	for _, filter := range filters {
		for window, walkErr := range windows {
			if walkErr != nil {
				return search.ExactPage{}, walkErr
			}
			page, err := c.searchExactWindows(ctx, search.ExactQuery{
				Terms: terms, AppID: filter.app, Domain: filter.domain,
				ExcludeAppIDs: []string{appidentity.BundleID},
				TopK:          search.MaxResults, MaxRows: search.MaxIndexRows, MaxBytes: search.MaxIndexBytes,
			}, singleSearchWindow(window))
			if err != nil {
				return search.ExactPage{}, err
			}
			// Reject lookalike labels inside each bounded physical result before
			// they can displace another scope page's authenticated exact subject.
			var limited bool
			page.Hits, limited, err = c.filterTechnicalSubjects(ctx, plan.TechnicalSubjects, page.Hits)
			if err != nil {
				return search.ExactPage{}, err
			}
			page.CoverageLimited = page.CoverageLimited || limited
			merged.Rows += page.Rows
			merged.Nodes += page.Nodes
			merged.HasMore = merged.HasMore || page.HasMore
			merged.IndexingPending = merged.IndexingPending || page.IndexingPending
			merged.CoverageLimited = merged.CoverageLimited || page.CoverageLimited
			for _, hit := range page.Hits {
				previous, found := best[hit.SourceID]
				if !found || hit.Score > previous.Score {
					if previous.CapturedAt.After(hit.CapturedAt) {
						hit.CapturedAt = previous.CapturedAt
					}
					best[hit.SourceID] = hit
				} else if hit.CapturedAt.After(previous.CapturedAt) {
					previous.CapturedAt = hit.CapturedAt
					best[hit.SourceID] = previous
				}
			}
			merged.Hits = merged.Hits[:0]
			for _, hit := range best {
				merged.Hits = append(merged.Hits, hit)
			}
			sort.Slice(merged.Hits, func(i, j int) bool {
				if merged.Hits[i].Score != merged.Hits[j].Score {
					return merged.Hits[i].Score > merged.Hits[j].Score
				}
				return merged.Hits[i].SourceID < merged.Hits[j].SourceID
			})
			if len(merged.Hits) > search.MaxResults {
				merged.HasMore = true
				for _, hit := range merged.Hits[search.MaxResults:] {
					delete(best, hit.SourceID)
				}
				merged.Hits = merged.Hits[:search.MaxResults]
			}
		}
	}
	// Per-page validation has separate bounded reads. Enforce the shared
	// accepted-source hydration budget after their verified hits are merged.
	var limited bool
	merged.Hits, limited, err = c.filterTechnicalSubjects(ctx, plan.TechnicalSubjects, merged.Hits)
	if err != nil {
		return search.ExactPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return search.ExactPage{}, err
	}
	merged.CoverageLimited = merged.CoverageLimited || limited
	if len(merged.Hits) > limit {
		merged.HasMore = true
		merged.Hits = merged.Hits[:limit]
	}
	for _, hit := range merged.Hits {
		merged.Bytes += hit.Bytes
	}
	return merged, nil
}
