package client

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/search"
)

// recencyEvidence lists the newest captures in the plan window. A "just now"
// question first looks at its own window; when that holds nothing it widens
// once to assistant.ScopeJustNowWiden and reports how far it looked, so the
// answer can say so instead of claiming an empty past hour. Authentication
// pages never answer a recency question, and Blur's own interface is already
// excluded from every recent-capture read.
func (c *Client) recencyEvidence(ctx context.Context, plan assistant.Plan, start, end time.Time, scoped []activity.Segment, limit int) (items []assistant.Evidence, widened time.Duration, segments []activity.Segment, err error) {
	justNow := plan.Scope.Label == assistant.ScopeJustNow
	items, err = c.recentPlanEvidence(ctx, plan, start, end, scoped, limit)
	if err != nil || !justNow {
		return items, 0, scoped, err
	}
	items = withoutAuthenticationPages(items)
	if len(items) > 0 {
		return items, 0, scoped, nil
	}
	wideStart := end.Add(-assistant.ScopeJustNowWiden)
	if _, ok := c.planActivity(); ok {
		wide, err := c.windowSegments(ctx, wideStart, end, MaxActivityReportSegments)
		if err != nil {
			return nil, 0, scoped, err
		}
		scoped = filterSegments(wide, plan)
	}
	items, err = c.recentPlanEvidence(ctx, plan, wideStart, end, scoped, limit)
	if err != nil {
		return nil, 0, scoped, err
	}
	return withoutAuthenticationPages(items), assistant.ScopeJustNowWiden, scoped, nil
}

// conversationEvidence gathers the saved conversation with one person: up to
// assistant.MaxEvidenceSources captures that are about the named person,
// messaging and mail apps first, listed oldest first so the recap reads as a
// timeline. A chat sidebar lists every contact once, so captures that only
// mention the name in passing are dropped when others are clearly about it.
func (c *Client) conversationEvidence(ctx context.Context, plan assistant.Plan, bounded bool, windowSegs []activity.Segment) ([]assistant.Evidence, bool, error) {
	if len(plan.Terms) == 0 || strings.TrimSpace(plan.Entity) == "" {
		return nil, false, nil
	}
	lexical := plan
	lexical.Literal = true // exact name matches only: a conversation recap is not a semantic search
	pool := min(planLookupPool, MaxSearchResults)
	page, err := c.planSearchCandidates(ctx, lexical, bounded, windowSegs, pool, pool, min(search.MaxIndexRows, pool*16))
	if err != nil {
		return nil, false, err
	}
	items, err := c.pageEvidence(ctx, lexical, page, windowSegs)
	if err != nil {
		return nil, false, err
	}
	about := assistant.DominantMentions(items, plan.Entity)
	counts := make(map[string]int, len(about))
	for _, item := range about {
		counts[item.SourceID] = assistant.NameMentions(item.Text, plan.Entity)
	}
	sort.SliceStable(about, func(i, j int) bool {
		mi, mj := isMessagingEvidence(about[i]), isMessagingEvidence(about[j])
		if mi != mj {
			return mi
		}
		if counts[about[i].SourceID] != counts[about[j].SourceID] {
			return counts[about[i].SourceID] > counts[about[j].SourceID]
		}
		return about[i].CapturedAt.After(about[j].CapturedAt)
	})
	limited := page.HasMore || len(about) > assistant.MaxEvidenceSources
	if len(about) > assistant.MaxEvidenceSources {
		about = about[:assistant.MaxEvidenceSources]
	}
	sort.SliceStable(about, func(i, j int) bool {
		if !about[i].CapturedAt.Equal(about[j].CapturedAt) {
			return about[i].CapturedAt.Before(about[j].CapturedAt)
		}
		return about[i].SourceID < about[j].SourceID
	})
	return about, limited || page.IndexingPending, nil
}

func isMessagingEvidence(item assistant.Evidence) bool {
	return appidentity.IsMessagingApp(item.AppID) || appidentity.IsMessagingApp(item.Domain)
}
