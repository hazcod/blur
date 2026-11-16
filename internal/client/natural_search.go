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

func (c *Client) naturalSearchPage(ctx context.Context, question string, limit int) (search.ExactPage, assistant.Plan, error) {
	if c == nil || c.data == nil || ctx == nil || limit < 1 || limit > MaxSearchResults || len(question) > MaxQueryBytes || !utf8.ValidString(question) || strings.ContainsRune(question, 0) || strings.TrimSpace(question) == "" {
		return search.ExactPage{}, assistant.Plan{}, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return search.ExactPage{}, assistant.Plan{}, err
	}
	apps, err := c.KnownApps(ctx)
	if ctx.Err() != nil {
		return search.ExactPage{}, assistant.Plan{}, ctx.Err()
	}
	// Static aliases and literal topic search remain usable with no historical
	// app directory. Unrecognized names stay topic terms, never broader filters.
	if err != nil {
		apps = nil
	}
	plan, err := assistant.PlanSearch(question, c.askClock(), apps)
	if err != nil {
		return search.ExactPage{}, plan, ErrInvalidRequest
	}
	if len(plan.Terms) == 0 && plan.Scope.IsZero() && len(plan.AppIDs)+len(plan.Domains) == 0 {
		plan.Terms, err = search.NormalizeTokens(question, search.MaxTerms)
		if err != nil || len(plan.Terms) == 0 {
			return search.ExactPage{}, plan, ErrInvalidRequest
		}
	}
	bounded := !plan.Scope.IsZero()
	var segments []activity.Segment
	if bounded {
		start, end := plan.Scope.Start, plan.Scope.End
		if end.Equal(start) {
			return search.ExactPage{}, plan, nil
		}
		if !end.After(start) || end.Sub(start) > storage.MaxObservationWindow {
			return search.ExactPage{}, plan, ErrActivityLimit
		}
		if _, ok := c.data.(ActivityData); ok {
			segments, err = c.windowSegments(ctx, start, end, MaxActivityReportSegments)
			if err != nil {
				return search.ExactPage{}, plan, err
			}
		}
	}
	if len(plan.Terms) > 0 {
		if !bounded && len(plan.AppIDs)+len(plan.Domains) == 0 {
			page, err := c.searchExactTerms(ctx, plan.Terms, limit, 0, limit)
			if err != nil {
				return search.ExactPage{}, plan, err
			}
			if plan.Literal {
				return page, plan, nil
			}
			page, err = c.hybridPage(ctx, question, page, semantic.Query{Limit: limit}, limit)
			return page, plan, err
		}
		pool := limit
		if bounded {
			pool = max(pool, planLookupPool)
		}
		rows := min(search.MaxIndexRows, pool*16)
		if !bounded && len(plan.AppIDs)+len(plan.Domains) == 0 {
			rows = pool
		}
		page, err := c.planSearchCandidates(ctx, plan, bounded, segments, limit, pool, rows)
		return page, plan, err
	}
	page, err := c.browseSearchScope(ctx, plan, limit)
	return page, plan, err
}

func (c *Client) browseSearchScope(ctx context.Context, plan assistant.Plan, limit int) (search.ExactPage, error) {
	reader, ok := c.data.(ObservationWindowReader)
	if !ok {
		return search.ExactPage{}, ErrNotConfigured
	}
	start, end := plan.Scope.Start, plan.Scope.End
	if plan.Scope.IsZero() {
		end = c.askClock()
		start = end.AddDate(0, 0, -planWindowDays)
	}
	if !end.After(start) {
		return search.ExactPage{}, nil
	}
	filters, err := memoryFilters(plan.AppIDs, plan.Domains)
	if err != nil {
		return search.ExactPage{}, err
	}

	best := map[string]search.IndexHit{}
	var result search.ExactPage
	for _, filter := range filters {
		page, err := readSourceWindow(ctx, reader, storage.ObservationWindowRequest{Start: start, End: end, AppID: filter.app, Domain: filter.domain, Limit: min(storage.MaxObservationPageRows, limit+1), MaxBytes: MaxSearchBytes, ExcludeAppIDs: []string{appidentity.BundleID}})
		if err != nil {
			return search.ExactPage{}, err
		}
		if len(page.CapturedAt) != len(page.Sources) {
			return search.ExactPage{}, ErrInvalidRequest
		}
		result.HasMore = result.HasMore || page.HasMore
		result.IndexingPending = result.IndexingPending || page.Pending
		for i, o := range page.Sources {
			at := page.CapturedAt[i]
			if at.Before(start) || !at.Before(end) || strings.EqualFold(o.AppID(), appidentity.BundleID) || filter.app != "" && o.AppID() != filter.app || filter.domain != "" && o.Domain() != filter.domain {
				return search.ExactPage{}, ErrInvalidRequest
			}
			hit := search.IndexHit{SourceID: o.SourceID(), Bytes: len(o.Text()) + len(o.AppID()) + len(o.Domain()), CapturedAt: at}
			if previous, found := best[hit.SourceID]; !found || at.After(previous.CapturedAt) {
				best[hit.SourceID] = hit
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
