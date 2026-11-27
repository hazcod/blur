package client

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage"
)

func (c *Client) naturalSearchPage(ctx context.Context, question string, limit int) (naturalPage, assistant.Plan, error) {
	if c == nil || c.data == nil || ctx == nil || limit < 1 || limit > MaxSearchResults || len(question) > MaxQueryBytes || !utf8.ValidString(question) || strings.ContainsRune(question, 0) || strings.TrimSpace(question) == "" {
		return naturalPage{}, assistant.Plan{}, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return naturalPage{}, assistant.Plan{}, err
	}
	apps, err := c.KnownApps(ctx)
	if ctx.Err() != nil {
		return naturalPage{}, assistant.Plan{}, ctx.Err()
	}
	// Static aliases and literal topic search remain usable with no historical
	// app directory. Unrecognized names stay topic terms, never broader filters.
	if err != nil {
		apps = nil
	}
	plan, err := assistant.PlanSearch(question, c.AskTime(), apps)
	if err != nil {
		return naturalPage{}, plan, ErrInvalidRequest
	}
	if len(plan.Terms) == 0 && plan.Scope.IsZero() && len(plan.AppIDs)+len(plan.Domains) == 0 {
		plan.Terms, err = search.NormalizeTokens(question, search.MaxTerms)
		if err != nil || len(plan.Terms) == 0 {
			return naturalPage{}, plan, ErrInvalidRequest
		}
	}
	bounded := !plan.Scope.IsZero()
	var segments []activity.Segment
	if bounded {
		start, end := plan.Scope.Start, plan.Scope.End
		if end.Equal(start) {
			return naturalPage{}, plan, nil
		}
		if !end.After(start) {
			return naturalPage{}, plan, ErrActivityLimit
		}
	}
	if len(plan.Terms) > 0 {
		page, err := c.rankedSearchPage(ctx, question, plan, bounded, segments, limit)
		return page, plan, err
	}
	page, err := c.browseSearchScope(ctx, plan, limit)
	return naturalPage{ExactPage: page}, plan, err
}

func (c *Client) browseSearchScope(ctx context.Context, plan assistant.Plan, limit int) (search.ExactPage, error) {
	reader, ok := c.data.(ObservationWindowReader)
	if !ok {
		return search.ExactPage{}, ErrNotConfigured
	}
	start, end := plan.Scope.Start, plan.Scope.End
	if plan.Scope.IsZero() {
		end = c.AskTime()
		start = end.AddDate(0, 0, -planWindowDays)
	}
	if !end.After(start) {
		return search.ExactPage{}, nil
	}
	windows, err := splitSearchWindows(start, end)
	if err != nil {
		return search.ExactPage{}, err
	}
	filters, err := memoryFilters(plan.AppIDs, plan.Domains)
	if err != nil {
		return search.ExactPage{}, err
	}
	best := map[string]search.IndexHit{}
	var result search.ExactPage
	used := 0
	for _, filter := range filters {
		stopped := false
		selected := map[string]bool{}
		for window, walkErr := range windows {
			if walkErr != nil {
				return search.ExactPage{}, walkErr
			}
			if stopped {
				break
			}
			request := storage.ObservationWindowRequest{Start: window.start, End: window.end, AppID: filter.app, Domain: filter.domain, Limit: min(storage.MaxObservationPageRows, limit+1), ExcludeAppIDs: []string{appidentity.BundleID}}
			for round := 0; round <= limit; round++ {
				if err := ctx.Err(); err != nil {
					return search.ExactPage{}, err
				}
				request.MaxBytes = MaxSearchBytes - used
				// The storage window requires room for one maximum-size source.
				// Stop before issuing an invalid smaller follow-up request. This
				// allowance is shared by every filter and child window.
				if request.MaxBytes < storage.MaxObservationTextBytes+128+253 {
					result.CoverageLimited = true
					result.HasMore = true
					stopped = true
					break
				}
				page, err := readSourceWindow(ctx, reader, request)
				if err != nil {
					return search.ExactPage{}, err
				}
				result.IndexingPending = result.IndexingPending || page.Pending
				for i, source := range page.Sources {
					at := page.CapturedAt[i]
					if at.Before(window.start) || !at.Before(window.end) || strings.EqualFold(source.AppID(), appidentity.BundleID) || filter.app != "" && source.AppID() != filter.app || filter.domain != "" && source.Domain() != filter.domain {
						return search.ExactPage{}, ErrInvalidRequest
					}
					size := len(source.Text()) + len(source.AppID()) + len(source.Domain())
					used += size
					hit := search.IndexHit{SourceID: source.SourceID(), Bytes: size, CapturedAt: at}
					if previous, found := best[hit.SourceID]; !found || at.After(previous.CapturedAt) {
						best[hit.SourceID] = hit
					}
					selected[hit.SourceID] = true
				}
				// Bound retained metadata too; each later window is strictly older.
				ordered := make([]search.IndexHit, 0, len(best))
				for _, hit := range best {
					ordered = append(ordered, hit)
				}
				sort.Slice(ordered, func(i, j int) bool {
					if !ordered[i].CapturedAt.Equal(ordered[j].CapturedAt) {
						return ordered[i].CapturedAt.After(ordered[j].CapturedAt)
					}
					return ordered[i].SourceID > ordered[j].SourceID
				})
				if len(ordered) > limit+1 {
					for _, hit := range ordered[limit+1:] {
						delete(best, hit.SourceID)
					}
					result.HasMore = true
				}
				if len(selected) > limit {
					result.HasMore = true
					stopped = true
					break
				}
				if !page.HasMore {
					break
				}
				result.HasMore = true
				if page.NextCapturedAt.IsZero() || page.NextSourceID == "" || page.NextCapturedAt.Equal(request.BeforeCapturedAt) && page.NextSourceID == request.BeforeSourceID || round == limit {
					result.CoverageLimited = true
					stopped = true
					break
				}
				request.BeforeCapturedAt, request.BeforeSourceID = page.NextCapturedAt, page.NextSourceID
			}
		}
	}
	for _, hit := range best {
		result.Hits = append(result.Hits, hit)
	}
	sort.Slice(result.Hits, func(i, j int) bool {
		if !result.Hits[i].CapturedAt.Equal(result.Hits[j].CapturedAt) {
			return result.Hits[i].CapturedAt.After(result.Hits[j].CapturedAt)
		}
		return result.Hits[i].SourceID > result.Hits[j].SourceID
	})
	result.Rows = len(result.Hits)
	if len(result.Hits) > limit {
		result.HasMore = true
		result.Hits = result.Hits[:limit]
	}
	for _, hit := range result.Hits {
		result.Bytes += hit.Bytes
	}
	if err := ctx.Err(); err != nil {
		return search.ExactPage{}, err
	}
	return result, nil
}

func searchSemanticScope(plan assistant.Plan) semantic.Query {
	return semantic.Query{AppIDs: plan.AppIDs, Domains: plan.Domains, Start: plan.Scope.Start, End: plan.Scope.End}
}

// Search keeps task-owned calendar/history constraints separate from capture
// time. Reminder due dates are not source visit timestamps.
func (c *Client) naturalTaskSearch(ctx context.Context, plan assistant.Plan, limit int) (taskSearchPage, error) {
	filterPlan := plan
	filterPlan.Question = plan.FilterQuestion
	query, ok := resolvedTaskQuery(filterPlan, c.askClock())
	if !ok {
		filterPlan.Question = "show " + filterPlan.Question
		query, ok = resolvedTaskQuery(filterPlan, c.askClock())
	}
	scope := searchSemanticScope(plan)
	terms := taskTopicTerms(plan.Terms)
	if ok {
		scope.Start, scope.End = time.Time{}, time.Time{}
		terms = taskInspectionTerms(plan)
		terms = uniqueSorted(append(terms, plan.LiteralTerms...))
		if query.Next {
			limit = 1
		}
		return c.searchTaskCandidates(ctx, plan.Question, terms, scope, limit, false, &query, plan.Literal, nil)
	}
	terms = uniqueSorted(append(terms, plan.LiteralTerms...))
	if len(terms) == 0 {
		return taskSearchPage{}, nil
	}
	return c.searchTaskCandidates(ctx, plan.Question, terms, scope, limit, false, nil, plan.Literal, nil)
}
