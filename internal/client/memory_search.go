package client

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

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
	CoverageLimited bool      `json:"coverage_limited,omitempty"`

	// text and terms let a byte budget re-cut the excerpt without a re-read.
	text  string
	terms []string
}

// naturalPage is an index page plus the sources that matched only some of the
// query terms.
type naturalPage struct {
	search.ExactPage
	// relaxed marks sources that carry only some of the query terms, or were
	// added by the optional meaning step.
	relaxed map[string]bool
	// semantic marks the sources the meaning step added after the core rows.
	semantic map[string]bool
	// rankers keeps each group's ranking input (false: all-terms, true:
	// majority-term) so the page can be re-ordered by term closeness once its
	// rows are hydrated, without reading any further source.
	rankers map[bool]lookupRankInput
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
	windows, err := c.searchScopeWindows(ctx, plan, bounded, segments)
	if err != nil {
		return naturalPage{}, err
	}
	var start, end time.Time
	if bounded {
		start, end, _, _ = c.planWindow(plan)
	}
	rows, pool := searchRowBudget(limit), searchPoolSize(limit)
	query := func(f memoryFilter, minMatches int, partialOnly bool, topK int) search.ExactQuery {
		return search.ExactQuery{Terms: terms, AppID: f.app, Domain: f.domain, ExcludeAppIDs: []string{appidentity.BundleID}, CapturedStart: start, CapturedEnd: end, MinTermMatches: minMatches, PartialOnly: partialOnly, TopK: topK, MaxRows: rows, MaxBytes: search.MaxIndexBytes}
	}
	var merged search.ExactPage
	exactBest := map[string]search.IndexHit{}
	for _, f := range filters {
		if err := ctx.Err(); err != nil {
			return naturalPage{}, err
		}
		page, err := c.searchExactWindows(ctx, query(f, 0, false, pool), windows)
		if errors.Is(err, search.ErrIndexBudget) && pool > limit {
			// The ranking pool could not be proven inside the row budget; fall
			// back to the page-sized prefix and report the gap.
			merged.CoverageLimited = true
			page, err = c.searchExactWindows(ctx, query(f, 0, false, limit), windows)
		}
		if err != nil {
			return naturalPage{}, err
		}
		merged.Rows += page.Rows
		merged.Nodes += page.Nodes
		merged.HasMore = merged.HasMore || page.HasMore
		merged.IndexingPending = merged.IndexingPending || page.IndexingPending
		merged.CoverageLimited = merged.CoverageLimited || page.CoverageLimited
		for _, hit := range page.Hits {
			if prev, ok := exactBest[hit.SourceID]; !ok || hit.Score > prev.Score {
				exactBest[hit.SourceID] = hit
			}
		}
	}
	partialBest := map[string]search.IndexHit{}
	relaxed := map[string]bool{}
	if (len(exactBest) < limit || len(plan.NumberedSubjects) > 0) && len(terms) >= 2 && !plan.Literal {
		minMatches := lookupMajorityTerms(len(terms))
	passes:
		for _, f := range filters {
			if err := ctx.Err(); err != nil {
				return naturalPage{}, err
			}
			page, err := c.searchExactWindows(ctx, query(f, minMatches, true, pool), windows)
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
			merged.CoverageLimited = merged.CoverageLimited || page.CoverageLimited
			for _, hit := range page.Hits {
				if _, isExact := exactBest[hit.SourceID]; isExact {
					continue
				}
				if prev, ok := partialBest[hit.SourceID]; !ok || hit.Score > prev.Score {
					partialBest[hit.SourceID] = hit
				}
			}
		}
	}
	// Sources that spell a compound of the query the other way hold its words
	// too ("threat-bot" for "threatbot"). They join the rows above with match
	// reports on the query's own terms, so a full match is not a closest match.
	variant, compoundLimited, err := c.compoundVariantFor(ctx, plan, question, compoundScope{filters: filters, windows: windows, start: start, end: end}, exactBest, partialBest)
	if err != nil {
		return naturalPage{}, err
	}
	merged.CoverageLimited = merged.CoverageLimited || compoundLimited
	if variant != nil {
		extra, limited, more, err := variant.hits(ctx, terms, pool, rows)
		if err != nil {
			return naturalPage{}, err
		}
		merged.CoverageLimited = merged.CoverageLimited || limited
		merged.HasMore = merged.HasMore || more
		mergeCompoundHits(exactBest, partialBest, extra, len(terms))
	}
	exact, partial := mapHits(exactBest), mapHits(partialBest) // deterministic input
	// A date can supply every independent token without supplying a numbered
	// subject. Read a single bounded pool for numeric phrase support before
	// cutting the page, and place unverified all-term rows with closest matches.
	var facts map[string]lookupSourceFacts
	if len(plan.NumberedSubjects) > 0 {
		candidates := append(slices.Clone(exact), partial...)
		facts, err = c.readLookupFacts(ctx, candidates, plan.NumberedSubjects...)
		if err != nil {
			return naturalPage{}, err
		}
		merged.CoverageLimited = merged.CoverageLimited || len(facts) < len(candidates)
		var supported []search.IndexHit
		for _, hit := range exact {
			if facts[hit.SourceID].numbered == len(plan.NumberedSubjects) {
				supported = append(supported, hit)
			} else {
				partial = append(partial, hit)
			}
		}
		exact = supported
	}
	rankers := map[bool]lookupRankInput{}
	exact, rankers[false], err = c.rankSearchGroup(ctx, plan, terms, exact, false, facts)
	if err != nil {
		return naturalPage{}, err
	}
	partial, rankers[true], err = c.rankSearchGroup(ctx, plan, terms, partial, true, facts)
	if err != nil {
		return naturalPage{}, err
	}
	for _, hit := range partial {
		relaxed[hit.SourceID] = true
	}
	core := append(append(make([]search.IndexHit, 0, len(exact)+len(partial)), exact...), partial...)
	merged.Hits = core
	merged.Bytes = 0
	semanticAdded := map[string]bool{}

	// The optional meaning step runs only behind the user's own gates (a
	// selected local model or the enabled memory index, both inside
	// hybridPage) and never for a keyword query that already has exact rows:
	// the exact core is authoritative. When it runs it can only append: core
	// rows keep their order and flags, additions follow them flagged as
	// closest matches, and summary rows are untouched.
	if !plan.Literal && len(core) < limit && (len(core) == 0 || naturalLanguageRecallQuery(question)) {
		minimumScore := float32(.3)
		if plan.MatchOnly {
			minimumScore = .55
		}
		extra, err := c.hybridScopedSearch(ctx, question, search.ExactPage{Rows: merged.Rows, Nodes: merged.Nodes, Hits: slices.Clone(core), IndexingPending: merged.IndexingPending, CoverageLimited: merged.CoverageLimited, HasMore: merged.HasMore}, semantic.Query{Limit: min(limit, semantic.MaxResults), AppIDs: plan.AppIDs, Domains: plan.Domains, Start: start, End: end}, windows, limit, false, minimumScore)
		if err != nil {
			return naturalPage{}, err
		}
		merged.Rows, merged.Nodes = extra.Rows, extra.Nodes
		merged.IndexingPending = merged.IndexingPending || extra.IndexingPending
		merged.CoverageLimited = merged.CoverageLimited || extra.CoverageLimited
		merged.HasMore = merged.HasMore || extra.HasMore
		known := make(map[string]bool, len(core))
		for _, hit := range core {
			known[hit.SourceID] = true
		}
		for _, hit := range extra.Hits {
			if known[hit.SourceID] {
				continue
			}
			known[hit.SourceID] = true
			merged.Hits = append(merged.Hits, hit)
			relaxed[hit.SourceID], semanticAdded[hit.SourceID] = true, true
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
	merged.MatchSpellings = variant.words()
	return naturalPage{ExactPage: merged, relaxed: relaxed, semantic: semanticAdded, rankers: rankers}, nil
}

// rankSearchGroup orders one candidate group with the Ask lookup ranking:
// entity and messaging boosts only when the query carries them, then matched
// terms, term rarity (read only for a majority-term group, where terms differ)
// and capture time newest first. Term closeness joins as a tie-break after
// hydration (see reorderByCloseness), never by reading more sources. Nothing
// is demoted or dropped here.
func (c *Client) rankSearchGroup(ctx context.Context, plan assistant.Plan, terms []string, hits []search.IndexHit, rarity bool, facts map[string]lookupSourceFacts) ([]search.IndexHit, lookupRankInput, error) {
	in := lookupRankInput{entityMask: lookupEntityMask(plan.Entity, terms), speech: questionHasSpeechVerb(plan.Question)}
	if len(plan.NumberedSubjects) > 0 {
		in.numbered = func(hit search.IndexHit) int { return facts[hit.SourceID].numbered }
	}
	if len(hits) < 2 {
		return hits, in, nil
	}
	if rarity {
		in.rarity = make([]float64, len(terms))
		for i, term := range terms {
			df, err := c.termDocumentFrequency(ctx, term)
			if err != nil {
				return nil, in, err
			}
			in.rarity[i] = lookupTermRarity(df, lookupRarityTotal)
		}
	}
	if in.speech {
		if facts == nil {
			var err error
			facts, err = c.readLookupFacts(ctx, hits)
			if err != nil {
				return nil, in, err
			}
		}
		in.isMessaging = func(id string) bool { return facts[id].conversational }
	}
	in.capturedAt = func(hit search.IndexHit) time.Time {
		at, unknown, err := c.sourceCaptureTime(ctx, hit.SourceID, hit.CapturedAt)
		if err != nil || unknown {
			return time.Time{}
		}
		return at
	}
	return rankLookupHits(hits, in), in, nil
}

// reorderByCloseness applies the closeness tie-break to the rows that are
// already hydrated for this page: within each group it re-runs the group's own
// ranking with closeness (the tightest window holding the query terms) added
// between term rarity and capture time. It reads no source beyond the page's
// own hydration, so it can only reorder rows that tie on every stronger key.
func reorderByCloseness(hits []SearchHit, page naturalPage, terms []string) {
	if len(terms) < 2 {
		return
	}
	closeness := make(map[string]int, len(hits))
	for _, hit := range hits {
		closeness[hit.SourceID] = termCloseness(hit.Text, terms)
	}
	for _, relaxed := range []bool{false, true} {
		in, ok := page.rankers[relaxed]
		if !ok {
			continue
		}
		in.closeness = func(hit search.IndexHit) int { return closeness[hit.SourceID] }
		var positions []int
		var group []search.IndexHit
		byID := map[string]SearchHit{}
		for i, hit := range hits {
			if hit.Task != nil || page.relaxed[hit.SourceID] != relaxed || page.semantic[hit.SourceID] {
				continue
			}
			positions = append(positions, i)
			group = append(group, search.IndexHit{SourceID: hit.SourceID, MatchedTerms: hit.matchedMask, CapturedAt: hit.CapturedAt})
			byID[hit.SourceID] = hit
		}
		if len(group) < 2 {
			continue
		}
		for i, ranked := range rankLookupHits(group, in) {
			hits[positions[i]] = byID[ranked.SourceID]
		}
	}
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
		// Thirty local calendar days in the configured chronicle zone, the
		// same zone the recap rollups are dated in.
		zone, zoneErr := loadZone(localZoneName())
		if zoneErr != nil {
			zone = time.UTC
		}
		now := c.askClock()
		start, end = now.In(zone).AddDate(0, 0, -searchSummaryDefaultDays), now.Add(time.Minute)
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
		return len(terms) == 0 || len(matchedQueryTerms(text, "", terms, 0)) >= minMatches
	}
	intersects := func(from, to time.Time) bool { return from.Before(end) && to.After(start) }
	var found []MemorySummaryHit

	examined := 0
	for _, level := range []string{chronicle.RollupLevelYear, chronicle.RollupLevelMonth, chronicle.RollupLevelWeek, chronicle.RollupLevelDay} {
		room := maxSearchSummaryRollups - examined
		if room < 1 {
			break
		}
		// A period that starts before the scope can still reach into it.
		widen := 2 * 24 * time.Hour
		if level == chronicle.RollupLevelWeek {
			widen = 8 * 24 * time.Hour
		} else if level == chronicle.RollupLevelMonth {
			widen = 32 * 24 * time.Hour
		} else if level == chronicle.RollupLevelYear {
			widen = 367 * 24 * time.Hour
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
				Excerpt: QueryExcerpt(row.Text, terms, 0, 0, searchExcerptBytes), SourceIDsSample: sampleSourceIDs(row.SourceIDs), Dirty: row.Dirty, CoverageLimited: row.Sampled || row.CoverageVersion == 0, text: row.Text, terms: terms,
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
