package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage/durable"
)

// recordingStore forwards to the real durable store and records what the search
// core asks of the index and the recap store.
type recordingStore struct {
	*durable.Store
	mu      sync.Mutex
	queries []search.ExactQuery
	// failPartial makes every majority-term query run out of row budget.
	failPartial bool
	rollupCalls []rollupCall
	recapPages  int
	// recapForever makes ListChronicles return one synthetic row and a fresh
	// cursor on every call, so only the page bound can stop the loop.
	recapForever bool
}

type rollupCall struct {
	level string
	limit int
	rows  int
}

func (r *recordingStore) SearchExact(ctx context.Context, q search.ExactQuery) (search.ExactPage, error) {
	r.mu.Lock()
	r.queries = append(r.queries, q)
	r.mu.Unlock()
	if r.failPartial && q.PartialOnly {
		return search.ExactPage{}, search.ErrIndexBudget
	}
	return r.Store.SearchExact(ctx, q)
}

func (r *recordingStore) ListChronicleRollups(ctx context.Context, level string, start, end time.Time, after string, limit int) ([]chronicle.Rollup, string, error) {
	rows, next, err := r.Store.ListChronicleRollups(ctx, level, start, end, after, limit)
	r.mu.Lock()
	r.rollupCalls = append(r.rollupCalls, rollupCall{level: level, limit: limit, rows: len(rows)})
	r.mu.Unlock()
	return rows, next, err
}

func (r *recordingStore) ListChronicles(ctx context.Context, start, end time.Time, after string, limit int) ([]chronicle.Summary, string, error) {
	r.mu.Lock()
	r.recapPages++
	page := r.recapPages
	r.mu.Unlock()
	if r.recapForever {
		at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(page) * time.Hour)
		row := chronicle.Summary{HourID: "hour:" + at.Format("20060102T15Z"), Start: at, End: at.Add(time.Hour), Text: "- filler", SourceIDs: []string{strings.Repeat("cd", 32)}}
		return []chronicle.Summary{row}, row.HourID, nil
	}
	return r.Store.ListChronicles(ctx, start, end, after, limit)
}

func (r *recordingStore) exactQueries() []search.ExactQuery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.queries)
}

func recordingClient(t *testing.T) (*scenarioCorpus, *Client, *recordingStore) {
	t.Helper()
	corpus := newScenarioCorpus(t)
	recorder := &recordingStore{Store: corpus.store}
	c := NewClient(recorder)
	c.askNow = func() time.Time { return corpus.now }
	t.Cleanup(c.Close)
	return corpus, c, recorder
}

func pageLabels(corpus *scenarioCorpus, page MemorySearchPage) (labels []string, relaxed []bool) {
	for _, hit := range page.Hits {
		labels = append(labels, corpus.label(hit.SourceID))
		relaxed = append(relaxed, hit.Relaxed)
	}
	return labels, relaxed
}

func TestSearchCoreRanksAllTermsHitsByCapturedTimeWithoutRelaxation(t *testing.T) {
	corpus, c, recorder := recordingClient(t)
	page, err := c.SearchMemoryPageContext(context.Background(), "pentest")
	if err != nil {
		t.Fatal(err)
	}
	labels, relaxed := pageLabels(corpus, page)
	want := []string{"notion-pentest-plan", "okta-login", "slack-dima-1", "slack-samira-2", "mail-chloe"}
	if !slices.Equal(labels, want) {
		t.Fatalf("one-term search order = %v, want newest capture first %v", labels, want)
	}
	if slices.Contains(relaxed, true) || page.Relaxed {
		t.Fatalf("a one-term query has nothing to relax: %v", relaxed)
	}
	queries := recorder.exactQueries()
	if len(queries) != 1 || queries[0].MinTermMatches != 0 || queries[0].PartialOnly {
		t.Fatalf("a one-term search must make one all-terms index query, got %+v", queries)
	}
	if !slices.Contains(queries[0].ExcludeAppIDs, appidentity.BundleID) {
		t.Fatalf("Blur's own interface must be excluded at the index: %+v", queries[0].ExcludeAppIDs)
	}
}

func TestSearchCoreBoundsCandidatePoolAndRows(t *testing.T) {
	_, c, recorder := recordingClient(t)
	for _, limit := range []int{1, 10, 20, 30, MaxSearchResults} {
		recorder.queries = nil
		if _, err := c.searchMemoryContext(context.Background(), "pentest", limit); err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		queries := recorder.exactQueries()
		if len(queries) == 0 {
			t.Fatalf("limit %d made no index query", limit)
		}
		for _, q := range queries {
			wantRows := min(search.MaxIndexRows, limit*16)
			if q.MaxRows != wantRows || q.MaxRows > search.MaxIndexRows || q.TopK < limit || q.TopK > search.MaxResults || q.TopK != min(search.MaxResults, limit*4) {
				t.Fatalf("limit %d: rows=%d topK=%d, want rows=%d and a pool of min(%d, limit*4)", limit, q.MaxRows, q.TopK, wantRows, search.MaxResults)
			}
		}
	}
}

func TestSearchCoreAppendsFlaggedMajorityMatchesAfterAllTermsHits(t *testing.T) {
	corpus, c, recorder := recordingClient(t)
	page, err := c.SearchMemoryPageContext(context.Background(), "dima retest")
	if err != nil {
		t.Fatal(err)
	}
	labels, relaxed := pageLabels(corpus, page)
	if len(labels) < 4 || labels[0] != "slack-dima-retest" || labels[1] != "notion-pentest-plan" || relaxed[0] || relaxed[1] {
		t.Fatalf("all-terms hits must lead: %v relaxed=%v", labels, relaxed)
	}
	for i := 2; i < len(labels); i++ {
		if !relaxed[i] {
			t.Fatalf("%s follows the all-terms hits but is not flagged relaxed", labels[i])
		}
	}
	if !page.Relaxed || slices.Contains(labels, "blur-self") {
		t.Fatalf("relaxed=%t labels=%v", page.Relaxed, labels)
	}
	// Relaxed rows are ranked as their own group, newest first among equals.
	if i, j := slices.Index(labels, "slack-samira-4"), slices.Index(labels, "slack-samira-1"); i > j {
		t.Fatalf("relaxed group is not ranked newest first: %v", labels)
	}
	queries := recorder.exactQueries()
	var partial int
	for _, q := range queries {
		if q.PartialOnly {
			partial++
			if q.MinTermMatches != lookupMajorityTerms(2) {
				t.Fatalf("majority pass asked for %d matches", q.MinTermMatches)
			}
		}
	}
	if partial != 1 {
		t.Fatalf("expected exactly one majority-term query, got %d in %+v", partial, queries)
	}
}

func TestSearchCoreSkipsRelaxationWhenThePageIsFullOrQuoted(t *testing.T) {
	corpus, c, recorder := recordingClient(t)
	page, err := c.searchMemoryPageContext(context.Background(), "dima retest", 2)
	if err != nil {
		t.Fatal(err)
	}
	labels, relaxed := pageLabels(corpus, page)
	if !slices.Equal(labels, []string{"slack-dima-retest", "notion-pentest-plan"}) || slices.Contains(relaxed, true) {
		t.Fatalf("a full all-terms page = %v relaxed=%v", labels, relaxed)
	}
	for _, q := range recorder.exactQueries() {
		if q.PartialOnly {
			t.Fatalf("a full page must not widen the search: %+v", q)
		}
	}
	recorder.queries = nil
	quoted, err := c.SearchMemoryPageContext(context.Background(), `"dima retest"`)
	if err != nil {
		t.Fatal(err)
	}
	if _, relaxed := pageLabels(corpus, quoted); slices.Contains(relaxed, true) {
		t.Fatalf("quoted text stays literal: %v", relaxed)
	}
	for _, q := range recorder.exactQueries() {
		if q.PartialOnly {
			t.Fatalf("a quoted search must not widen: %+v", q)
		}
	}
}

func TestSearchCoreKeepsAllTermsHitsWhenTheWideningRunsOutOfBudget(t *testing.T) {
	corpus, c, recorder := recordingClient(t)
	recorder.failPartial = true
	page, err := c.SearchMemoryPageContext(context.Background(), "dima retest")
	if err != nil {
		t.Fatal(err)
	}
	labels, _ := pageLabels(corpus, page)
	if !slices.Equal(labels, []string{"slack-dima-retest", "notion-pentest-plan"}) || !page.CoverageLimited || !page.HasMore {
		t.Fatalf("labels=%v coverageLimited=%t hasMore=%t", labels, page.CoverageLimited, page.HasMore)
	}
}

func TestSearchCoreReportsHasMoreAndCutsToTheLimit(t *testing.T) {
	corpus, c, _ := recordingClient(t)
	page, err := c.searchMemoryPageContext(context.Background(), "pentest", 2)
	if err != nil {
		t.Fatal(err)
	}
	labels, _ := pageLabels(corpus, page)
	if !slices.Equal(labels, []string{"notion-pentest-plan", "okta-login"}) || !page.HasMore {
		t.Fatalf("labels=%v hasMore=%t", labels, page.HasMore)
	}
}

func TestSearchCoreDoesNotDemoteSignInPages(t *testing.T) {
	corpus, c, _ := recordingClient(t)
	page, err := c.SearchMemoryPageContext(context.Background(), "okta dima")
	if err != nil {
		t.Fatal(err)
	}
	labels, _ := pageLabels(corpus, page)
	if !slices.Contains(labels, "okta-login") {
		t.Fatalf("search is not an answer: the sign-in page must stay: %v", labels)
	}
}

func TestSearchCoreFillsPreviewsAndMatchedTerms(t *testing.T) {
	_, c, _ := recordingClient(t)
	page, err := c.SearchMemoryPageContext(context.Background(), "dima retest")
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range page.Hits {
		if hit.Excerpt == "" || len(hit.Excerpt) > searchExcerptBytes || len(hit.MatchedTerms) == 0 || hit.Text == "" {
			t.Fatalf("hit %.12s lacks a bounded preview or matched terms: excerpt=%q terms=%v", hit.SourceID, hit.Excerpt, hit.MatchedTerms)
		}
		if hit.Relaxed == (len(hit.MatchedTerms) == 2) {
			t.Fatalf("hit %.12s relaxed=%t but matched %v", hit.SourceID, hit.Relaxed, hit.MatchedTerms)
		}
	}
	if !strings.Contains(page.Hits[0].Excerpt, "retest") {
		t.Fatalf("the top hit's preview should show the retest line: %q", page.Hits[0].Excerpt)
	}
}

// putTestRollup stores a clean rollup the way the rollup worker does: derived
// text bound to source IDs, written at the revision the store already holds.
func putTestRollup(t *testing.T, corpus *scenarioCorpus, id, text string, children, labels []string) chronicle.Rollup {
	t.Helper()
	ctx := context.Background()
	rollup, err := chronicle.NewRollupPlaceholder(id, "Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	if existing, found, err := corpus.store.GetChronicleRollup(ctx, id); err != nil {
		t.Fatal(err)
	} else if found {
		rollup.Revision = existing.Revision
	}
	var sources []string
	for _, label := range labels {
		sources = append(sources, corpus.ids[label])
	}
	slices.Sort(sources)
	sum := sha256.Sum256([]byte(text))
	rollup.Text, rollup.ChildIDs, rollup.SourceIDs = text, children, sources
	rollup.GeneratedAt, rollup.Model, rollup.InputHash, rollup.Dirty = corpus.now, "local-model", hex.EncodeToString(sum[:]), false
	if err := corpus.store.PutChronicleRollup(ctx, rollup); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	stored, found, err := corpus.store.GetChronicleRollup(ctx, id)
	if err != nil || !found || stored.Text != text || stored.Dirty {
		t.Fatalf("seeded rollup %s did not stick: found=%t dirty=%t text=%q err=%v", id, found, stored.Dirty, stored.Text, err)
	}
	return stored
}

func seedRollupFor(t *testing.T, corpus *scenarioCorpus, id, text string, labels ...string) chronicle.Rollup {
	t.Helper()
	children := []string{"hour:20261110T10Z"}
	if strings.HasPrefix(id, "week:") {
		children = []string{"day:2026-11-10"}
	}
	return putTestRollup(t, corpus, id, text, children, labels)
}

func allLabels(corpus *scenarioCorpus) []string {
	var labels []string
	for label := range corpus.ids {
		labels = append(labels, label)
	}
	slices.Sort(labels)
	return labels
}

func TestSearchSummariesAreCappedNewestFirstAndCarrySourceIDs(t *testing.T) {
	corpus, c, _ := recordingClient(t)
	labels := allLabels(corpus)
	for day := 8; day <= 14; day++ {
		seedRollupFor(t, corpus, fmt.Sprintf("day:2026-11-%02d", day), fmt.Sprintf("- Quarterly planning workshop notes for day %d.", day), labels[:12]...)
	}
	page, err := c.SearchMemoryPageContext(context.Background(), "planning workshop")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, summary := range page.Summaries {
		ids = append(ids, summary.ID)
		if summary.Level != "day" || summary.Period != strings.TrimPrefix(summary.ID, "chronicle:day:") || summary.Excerpt == "" || summary.Start.IsZero() || !summary.End.After(summary.Start) {
			t.Fatalf("summary shape: %+v", summary)
		}
		if len(summary.SourceIDsSample) != searchSummarySourceSample {
			t.Fatalf("a rollup with 12 sources must sample %d, got %d", searchSummarySourceSample, len(summary.SourceIDsSample))
		}
		for _, id := range summary.SourceIDsSample {
			if corpus.label(id) == "" {
				t.Fatalf("summary names an unknown source %q", id)
			}
		}
	}
	want := []string{"chronicle:day:2026-11-14", "chronicle:day:2026-11-13", "chronicle:day:2026-11-12", "chronicle:day:2026-11-11", "chronicle:day:2026-11-10"}
	if !slices.Equal(ids, want) {
		t.Fatalf("summaries = %v, want the five newest %v", ids, want)
	}
}

func TestSearchSummariesNeedMajorityTermsAndSourceIDs(t *testing.T) {
	corpus, c, _ := recordingClient(t)
	seedRollupFor(t, corpus, "day:2026-11-12", "- Wrote the quarterly budget memo.", "note-buildwise")
	seedRollupFor(t, corpus, "day:2026-11-13", "- Quarterly planning only.", "note-buildwise")
	page, err := c.SearchMemoryPageContext(context.Background(), "quarterly budget memo")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, summary := range page.Summaries {
		ids = append(ids, summary.ID)
	}
	// "quarterly planning" carries 1 of 3 terms: below the majority of two.
	if !slices.Equal(ids, []string{"chronicle:day:2026-11-12"}) {
		t.Fatalf("summaries = %v", ids)
	}
}

func TestSearchSummariesFollowTheQueryTimeScope(t *testing.T) {
	corpus, c, _ := recordingClient(t)
	seedRollupFor(t, corpus, "day:2026-11-14", "- A Saturday of pentest findings.", "slack-dima-1")
	seedRollupFor(t, corpus, "day:2026-11-15", "- A Sunday of retest scoping.", "slack-dima-retest")
	page, err := c.SearchMemoryPageContext(context.Background(), "yesterday")
	if err != nil {
		t.Fatal(err)
	}
	var days []string
	for _, summary := range page.Summaries {
		if summary.Level == "day" {
			days = append(days, summary.Period)
		}
	}
	if !slices.Equal(days, []string{"2026-11-15"}) {
		t.Fatalf("'yesterday' (Sunday 15 November) must only see that day's rollup, got %v", days)
	}
	for _, summary := range page.Summaries {
		if !summary.End.After(time.Date(2026, 11, 14, 23, 0, 0, 0, time.UTC)) || summary.Start.After(time.Date(2026, 11, 15, 23, 0, 0, 0, time.UTC)) {
			t.Fatalf("summary %s lies outside the scope", summary.ID)
		}
	}
}

func TestSearchSummariesIncludeHourRecapsAndWeekRollups(t *testing.T) {
	corpus, c, _ := recordingClient(t)
	seedRollupFor(t, corpus, "week:2026-W46", "- A retest week.", "slack-dima-retest")
	page, err := c.SearchMemoryPageContext(context.Background(), "retest")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, summary := range page.Summaries {
		ids = append(ids, summary.ID)
		if len(summary.SourceIDsSample) == 0 {
			t.Fatalf("%s has no source IDs", summary.ID)
		}
	}
	if !slices.Contains(ids, "chronicle:week:2026-W46") || !slices.Contains(ids, "chronicle:hour:20261115T13Z") {
		t.Fatalf("summaries = %v", ids)
	}
}

func TestSearchSummariesDoNotApplyToAppOrSiteScopedQueries(t *testing.T) {
	_, c, _ := recordingClient(t)
	page, err := c.SearchMemoryPageContext(context.Background(), "retest in Slack")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Summaries) != 0 {
		t.Fatalf("recaps cannot be filtered by app: %+v", page.Summaries)
	}
}

func TestSearchSummariesReadBoundedRollupsAndRecapPages(t *testing.T) {
	_, c, recorder := recordingClient(t)
	recorder.recapForever = true
	if _, err := c.SearchMemoryPageContext(context.Background(), "retest"); err != nil {
		t.Fatalf("an endless recap cursor must stop at the page bound, not fail the search: %v", err)
	}
	if recorder.recapPages != maxSearchSummaryRecapPages {
		t.Fatalf("read %d recap pages, want exactly the %d page bound", recorder.recapPages, maxSearchSummaryRecapPages)
	}
	total := 0
	for _, call := range recorder.rollupCalls {
		if call.limit < 1 || call.limit > maxSearchSummaryRollups {
			t.Fatalf("rollup read limit %d outside 1..%d", call.limit, maxSearchSummaryRollups)
		}
		total += call.rows
	}
	if total > maxSearchSummaryRollups || len(recorder.rollupCalls) > 2 {
		t.Fatalf("rollup reads = %+v", recorder.rollupCalls)
	}
}

func TestSearchSummaryFailureDegradesInsteadOfHidingCaptureMatches(t *testing.T) {
	corpus, c, _ := recordingClient(t)
	broken := &brokenRecaps{Store: corpus.store}
	c2 := NewClient(broken)
	c2.askNow = func() time.Time { return corpus.now }
	t.Cleanup(c2.Close)
	page, err := c2.SearchMemoryPageContext(context.Background(), "pentest")
	if err != nil || len(page.Hits) == 0 || len(page.Summaries) != 0 || !page.CoverageLimited {
		t.Fatalf("hits=%d summaries=%d coverageLimited=%t err=%v", len(page.Hits), len(page.Summaries), page.CoverageLimited, err)
	}
	_ = c
}

type brokenRecaps struct{ *durable.Store }

func (b *brokenRecaps) ListChronicles(context.Context, time.Time, time.Time, string, int) ([]chronicle.Summary, string, error) {
	return nil, "", errors.New("recap store unavailable")
}
