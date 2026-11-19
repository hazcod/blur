package client

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage"
)

// The shared ranked search core serves the native Search page and the MCP
// search_memory tool. It never calls a model on the exact-match path: the
// optional meaning step stays behind hybridPage exactly as before.
const (
	// searchRowFactor sizes the index row budget: min(MaxIndexRows, limit*16).
	searchRowFactor = 16
	// searchPoolFactor sizes the ranked candidate pool above one page, so a
	// ranking by term closeness and capture time sees more than the score
	// prefix. It is capped at the index's own top-k ceiling.
	searchPoolFactor = 4

	maxSearchSummaries         = 5
	maxSearchSummaryRollups    = 64
	maxSearchSummaryRecapPages = maxRecapChroniclePages
	searchSummarySourceSample  = 8
	searchSummaryDefaultDays   = 30
)

// MemorySummaryHit is an index row for a day or week rollup or an hour recap.
// It is derived text, never evidence: SourceIDsSample names captures that
// resolve through get_snapshots, and ID plus Level and Period address the
// recap through get_chronicle.
type MemorySummaryHit struct {
	// ID is "chronicle:day:2026-11-15", "chronicle:week:2026-W46" or
	// "chronicle:hour:20261115T13Z".
	ID string `json:"id"`
	// Level is "hour", "day" or "week".
	Level string `json:"level"`
	// Period is the day key, the ISO week key, or for an hour the hour ID
	// ("hour:20261115T13Z") that get_chronicle takes as hour_id.
	Period          string    `json:"period"`
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	Excerpt         string    `json:"excerpt"`
	SourceIDsSample []string  `json:"source_ids"`
	Dirty           bool      `json:"dirty,omitempty"`

	// text and terms let a byte budget re-cut the excerpt without a re-read.
	text  string
	terms []string
}

// naturalPage is an index page plus the sources that matched only some of the
// query terms.
type naturalPage struct {
	search.ExactPage
	relaxed map[string]bool
}

// searchPoolSize is how many ranked candidates one index query returns.
func searchPoolSize(limit int) int { return min(search.MaxResults, limit*searchPoolFactor) }

// searchRowBudget is the index row budget of one query.
func searchRowBudget(limit int) int { return min(search.MaxIndexRows, limit*searchRowFactor) }

// rankedSearchPage runs the shared core for a plan that has terms: (1) an
// all-terms exact pass, ranked; (2) when the page is not full, a majority-term
// pass whose hits are flagged relaxed and ranked as their own group; (3) the
// optional meaning step; (4) a cut to limit. Blur's own interface is excluded
// at the index. Sign-in pages are not demoted: search is a search, not an
// answer.
func (c *Client) rankedSearchPage(ctx context.Context, question string, plan assistant.Plan, bounded bool, segments []activity.Segment, limit int) (naturalPage, error) {
	terms := plan.Terms
	if len(terms) > search.MaxTerms {
		terms = terms[:search.MaxTerms]
	}
	filters, err := memoryFilters(plan.AppIDs, plan.Domains)
	if err != nil {
		return naturalPage{}, err
	}
	var start, end time.Time
	var scopeSources []string
	if bounded {
		start, end, _, _ = c.planWindow(plan)
		var ids []string
		for _, segment := range segments {
			ids = append(ids, segment.SourceIDs...)
		}
		scopeSources = uniqueSorted(ids)
		if len(scopeSources) > search.MaxScopeSourceIDs {
			return naturalPage{}, ErrActivityLimit
		}
	}
	rows, pool := searchRowBudget(limit), searchPoolSize(limit)
	query := func(f memoryFilter, minMatches int, partialOnly bool, topK int) search.ExactQuery {
		return search.ExactQuery{Terms: terms, AppID: f.app, Domain: f.domain, ExcludeAppIDs: []string{appidentity.BundleID}, CapturedStart: start, CapturedEnd: end, ScopeSourceIDs: scopeSources, MinTermMatches: minMatches, PartialOnly: partialOnly, TopK: topK, MaxRows: rows, MaxBytes: search.MaxIndexBytes}
	}
	var merged search.ExactPage
	exactBest := map[string]search.IndexHit{}
	for _, f := range filters {
		if err := ctx.Err(); err != nil {
			return naturalPage{}, err
		}
		page, err := c.data.SearchExact(ctx, query(f, 0, false, pool))
		if errors.Is(err, search.ErrIndexBudget) && pool > limit {
			// The ranking pool could not be proven inside the row budget; fall
			// back to the page-sized prefix and report the gap.
			merged.CoverageLimited = true
			page, err = c.data.SearchExact(ctx, query(f, 0, false, limit))
		}
		if err != nil {
			return naturalPage{}, err
		}
		merged.Rows += page.Rows
		merged.Nodes += page.Nodes
		merged.HasMore = merged.HasMore || page.HasMore
		merged.IndexingPending = merged.IndexingPending || page.IndexingPending
		for _, hit := range page.Hits {
			if prev, ok := exactBest[hit.SourceID]; !ok || hit.Score > prev.Score {
				exactBest[hit.SourceID] = hit
			}
		}
	}
	exact := make([]search.IndexHit, 0, len(exactBest))
	for _, hit := range exactBest {
		exact = append(exact, hit)
	}
	sort.Slice(exact, func(i, j int) bool { return exact[i].SourceID < exact[j].SourceID }) // deterministic input
	exact, err = c.rankSearchGroup(ctx, plan, terms, exact, false)
	if err != nil {
		return naturalPage{}, err
	}

	var partial []search.IndexHit
	relaxed := map[string]bool{}
	if len(exact) < limit && len(terms) >= 2 && !plan.Literal {
		partialBest := map[string]search.IndexHit{}
		minMatches := lookupMajorityTerms(len(terms))
	passes:
		for _, f := range filters {
			if err := ctx.Err(); err != nil {
				return naturalPage{}, err
			}
			page, err := c.data.SearchExact(ctx, query(f, minMatches, true, pool))
			if errors.Is(err, search.ErrIndexBudget) {
				// The majority-term pass is an optional widening: keep the
				// all-terms hits and report the gap instead of failing.
				merged.CoverageLimited, merged.HasMore = true, true
				break passes
			}
			if err != nil {
				return naturalPage{}, err
			}
			merged.Rows += page.Rows
			merged.Nodes += page.Nodes
			merged.HasMore = merged.HasMore || page.HasMore
			merged.IndexingPending = merged.IndexingPending || page.IndexingPending
			for _, hit := range page.Hits {
				if _, isExact := exactBest[hit.SourceID]; isExact {
					continue
				}
				if prev, ok := partialBest[hit.SourceID]; !ok || hit.Score > prev.Score {
					partialBest[hit.SourceID] = hit
				}
			}
		}
		for _, hit := range partialBest {
			partial = append(partial, hit)
		}
		sort.Slice(partial, func(i, j int) bool { return partial[i].SourceID < partial[j].SourceID })
		partial, err = c.rankSearchGroup(ctx, plan, terms, partial, true)
		if err != nil {
			return naturalPage{}, err
		}
		for _, hit := range partial {
			relaxed[hit.SourceID] = true
		}
	}
	merged.Hits = append(append(make([]search.IndexHit, 0, len(exact)+len(partial)), exact...), partial...)
	merged.Bytes = 0

	if !plan.Literal {
		minimumScore := float32(.3)
		if plan.MatchOnly {
			minimumScore = .55
		}
		merged, err = c.hybridPage(ctx, question, merged, semantic.Query{Limit: min(limit, semantic.MaxResults), AppIDs: plan.AppIDs, Domains: plan.Domains, Start: start, End: end, ScopeSourceIDs: scopeSources}, limit, minimumScore)
		if err != nil {
			return naturalPage{}, err
		}
	}
	if len(merged.Hits) > limit {
		merged.HasMore = true
		merged.Hits = merged.Hits[:limit]
	}
	merged.Bytes = 0
	for _, hit := range merged.Hits {
		merged.Bytes += hit.Bytes
	}
	return naturalPage{ExactPage: merged, relaxed: relaxed}, nil
}

// rankSearchGroup orders one candidate group with the Ask lookup ranking:
// matched-term count, term rarity (read only for a majority-term group, where
// terms differ), term closeness, then capture time newest first. Entity and
// speech boosts apply only when the query carries them. Nothing is demoted or
// dropped here.
func (c *Client) rankSearchGroup(ctx context.Context, plan assistant.Plan, terms []string, hits []search.IndexHit, rarity bool) ([]search.IndexHit, error) {
	if len(hits) < 2 {
		return hits, nil
	}
	in := lookupRankInput{entityMask: lookupEntityMask(plan.Entity, terms), speech: questionHasSpeechVerb(plan.Question)}
	if rarity {
		in.rarity = make([]float64, len(terms))
		for i, term := range terms {
			df, err := c.termDocumentFrequency(ctx, term)
			if err != nil {
				return nil, err
			}
			in.rarity[i] = lookupTermRarity(df, lookupRarityTotal)
		}
	}
	if len(terms) >= 2 {
		// With one term every hit's closeness is that term's own width, so the
		// reads would change nothing.
		closeness, err := c.readTermCloseness(ctx, hits, terms)
		if err != nil {
			return nil, err
		}
		in.closeness = func(hit search.IndexHit) int { return closeness[hit.SourceID] }
	}
	if in.speech {
		facts, err := c.readLookupFacts(ctx, hits)
		if err != nil {
			return nil, err
		}
		in.isMessaging = func(id string) bool { return facts[id].conversational }
	}
	in.capturedAt = func(hit search.IndexHit) time.Time {
		at, unknown, err := c.sourceCaptureTime(ctx, hit.SourceID, time.Time{})
		if err != nil || unknown {
			return time.Time{}
		}
		return at
	}
	return rankLookupHits(hits, in), nil
}

// readTermCloseness reads each candidate once, inside the same aggregate byte
// ceiling hydration uses, and scores how tightly its query terms sit together.
// Candidates past the budget are unscored and rank after scored ones.
func (c *Client) readTermCloseness(ctx context.Context, hits []search.IndexHit, terms []string) (map[string]int, error) {
	scores := make(map[string]int, len(hits))
	used := 0
	for _, hit := range hits {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if hit.Bytes < 1 || hit.Bytes > MaxSearchBytes-used {
			continue
		}
		source, found, err := storage.ReadSourceEvidence(ctx, c.data, hit.SourceID)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		used += hit.Bytes
		scores[hit.SourceID] = termCloseness(source.Text(), terms)
	}
	return scores, nil
}

// termCloseness is the byte width of the tightest window of text that holds
// every query term the text contains. Zero means no term occurs (unknown).
func termCloseness(text string, terms []string) int {
	index := make(map[string]int, len(terms))
	for i, term := range terms {
		index[term] = i
	}
	type occurrence struct{ start, end, term int }
	var found []occurrence
	present := map[int]bool{}
	tokenStart := -1
	flush := func(offset int) {
		if tokenStart < 0 {
			return
		}
		if i, ok := index[strings.ToLower(text[tokenStart:offset])]; ok {
			found = append(found, occurrence{tokenStart, offset, i})
			present[i] = true
		}
		tokenStart = -1
	}
	for offset, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if tokenStart < 0 {
				tokenStart = offset
			}
		} else {
			flush(offset)
		}
	}
	flush(len(text))
	if len(found) == 0 {
		return 0
	}
	need, have, left := len(present), 0, 0
	counts := make(map[int]int, need)
	best := math.MaxInt
	for _, occ := range found {
		if counts[occ.term] == 0 {
			have++
		}
		counts[occ.term]++
		for have == need {
			best = min(best, occ.end-found[left].start)
			counts[found[left].term]--
			if counts[found[left].term] == 0 {
				have--
			}
			left++
		}
	}
	return max(best, 1)
}

// searchSummaries returns day and week rollups and hour recaps whose window
// intersects the query's time scope (the last 30 days unless the plan names a
// scope) and whose text carries a majority of the query terms. A scope-only
// query matches every intersecting summary. Reads are bounded to 64 rollups and
// 32 recap pages; rows without source IDs are never returned, they are capped
// at five, newest first, and they never count against the capture limit.
func (c *Client) searchSummaries(ctx context.Context, plan assistant.Plan, terms []string) ([]MemorySummaryHit, error) {
	if c.chronicles == nil || len(plan.AppIDs)+len(plan.Domains) > 0 {
		return nil, nil
	}
	var start, end time.Time
	if plan.Scope.IsZero() {
		if len(terms) == 0 {
			return nil, nil
		}
		now := c.askClock()
		start, end = now.AddDate(0, 0, -searchSummaryDefaultDays), now.Add(time.Minute)
	} else {
		start, end = plan.Scope.Start, plan.Scope.End
	}
	if !end.After(start) {
		return nil, nil
	}
	if end.Sub(start) > storage.MaxObservationWindow {
		start = end.Add(-storage.MaxObservationWindow)
	}
	minMatches := lookupMajorityTerms(len(terms))
	matches := func(text string) bool {
		return len(terms) == 0 || len(matchedQueryTerms(text, terms, 0)) >= minMatches
	}
	intersects := func(from, to time.Time) bool { return from.Before(end) && to.After(start) }
	var found []MemorySummaryHit

	examined := 0
	for _, level := range []string{chronicle.RollupLevelDay, chronicle.RollupLevelWeek} {
		room := maxSearchSummaryRollups - examined
		if room < 1 {
			break
		}
		// A period that starts before the scope can still reach into it.
		widen := 2 * 24 * time.Hour
		if level == chronicle.RollupLevelWeek {
			widen = 8 * 24 * time.Hour
		}
		rows, _, err := c.chronicles.ListChronicleRollups(ctx, level, start.Add(-widen), end, "", room)
		if err != nil {
			return nil, err
		}
		if len(rows) > room {
			return nil, ErrInvalidRequest
		}
		examined += len(rows)
		for _, row := range rows {
			if row.Level != level || !intersects(row.Start, row.End) || strings.TrimSpace(row.Text) == "" || len(row.SourceIDs) == 0 || !matches(row.Text) {
				continue
			}
			found = append(found, MemorySummaryHit{
				ID: "chronicle:" + row.ID, Level: level, Period: row.PeriodKey, Start: row.Start, End: row.End,
				Excerpt: QueryExcerpt(row.Text, terms, 0, 0, searchExcerptBytes), SourceIDsSample: sampleSourceIDs(row.SourceIDs), Dirty: row.Dirty, text: row.Text, terms: terms,
			})
		}
	}

	after, seen := "", map[string]bool{}
	for page := 0; ; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rows, next, err := c.chronicles.ListChronicles(ctx, start.Add(-time.Hour).UTC(), end.UTC(), after, planRecapChronicle)
		if err != nil {
			return nil, err
		}
		if len(rows) > planRecapChronicle {
			return nil, ErrInvalidRequest
		}
		for _, row := range rows {
			if !intersects(row.Start, row.End) || strings.TrimSpace(row.Text) == "" || len(row.SourceIDs) == 0 || !matches(row.Text) {
				continue
			}
			found = append(found, MemorySummaryHit{
				ID: "chronicle:" + row.HourID, Level: "hour", Period: row.HourID, Start: row.Start, End: row.End,
				Excerpt: QueryExcerpt(row.Text, terms, 0, 0, searchExcerptBytes), SourceIDsSample: sampleSourceIDs(row.SourceIDs), text: row.Text, terms: terms,
			})
		}
		if next == "" {
			break
		}
		if len(rows) == 0 || next == after || seen[next] || page+1 >= maxSearchSummaryRecapPages {
			break // bounded: the newest summaries already fit the cap
		}
		seen[next], after = true, next
	}

	sort.SliceStable(found, func(i, j int) bool {
		if !found[i].End.Equal(found[j].End) {
			return found[i].End.After(found[j].End)
		}
		if !found[i].Start.Equal(found[j].Start) {
			return found[i].Start.Before(found[j].Start) // the wider period first
		}
		return found[i].ID < found[j].ID
	})
	if len(found) > maxSearchSummaries {
		found = found[:maxSearchSummaries]
	}
	return found, nil
}

func sampleSourceIDs(ids []string) []string {
	return append([]string(nil), ids[:min(len(ids), searchSummarySourceSample)]...)
}
