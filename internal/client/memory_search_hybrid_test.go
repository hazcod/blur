package client

import (
	"context"
	"math"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/semantic"
)

// meaningStub is a memory index whose only match is the Venice shell capture,
// whatever the question. It counts how often the meaning step runs. The queries
// that expect its match carry a time scope ("last week") so the index scans
// exactly instead of walking its approximate graph, which a lone outlier vector
// among identical ones can miss.
type meaningStub struct {
	queries atomic.Int64
	match   string
}

func (e *meaningStub) Identity(context.Context) (semantic.Model, error) {
	return semantic.Model{Name: "stub", Digest: strings.Repeat("a", 64), Dimensions: 2, Format: semantic.FormatPlain}, nil
}

func (e *meaningStub) EmbedQuery(context.Context, semantic.Model, string) ([]float32, error) {
	e.queries.Add(1)
	return []float32{1, 0}, nil
}

func (e *meaningStub) EmbedDocuments(_ context.Context, _ semantic.Model, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		score := float32(.1)
		if strings.Contains(strings.ToLower(text), e.match) {
			score = .9
		}
		vectors[i] = []float32{score, float32(math.Sqrt(1 - float64(score*score)))}
	}
	return vectors, nil
}

func withMeaningStub(t *testing.T, corpus *scenarioCorpus, c *Client) *meaningStub {
	t.Helper()
	stub := &meaningStub{match: "venice"}
	service, err := semantic.NewService(corpus.store, corpus.store, stub, semantic.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.memoryIndex = &memoryIndexController{settings: DefaultMemoryIndexSettings(), service: service}
	t.Cleanup(service.Close)
	stub.queries.Store(0)
	return stub
}

func TestSearchNeverCallsTheMeaningStepForAKeywordQueryWithExactHits(t *testing.T) {
	corpus, c, _ := recordingClient(t)
	stub := withMeaningStub(t, corpus, c)
	model := &singleSearchModel{}
	c.localAI = &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}, askModel: model, service: notes.NewService(corpus.store, nil, nil)}

	for _, query := range []string{"pentest", "dima retest", "buildwise packet url"} {
		page, err := c.SearchMemoryPageContext(context.Background(), query)
		if err != nil || len(page.Hits) == 0 {
			t.Fatalf("%q: hits=%d err=%v", query, len(page.Hits), err)
		}
		for _, hit := range page.Hits {
			if hit.Semantic {
				t.Fatalf("%q: a keyword query with exact hits got a meaning match: %s", query, corpus.label(hit.SourceID))
			}
		}
	}
	if stub.queries.Load() != 0 || model.calls != 0 || model.answerCalls != 0 {
		t.Fatalf("keyword queries with exact hits reached the meaning step: embeddings=%d model calls=%d/%d", stub.queries.Load(), model.calls, model.answerCalls)
	}
}

func TestSearchMeaningStepOnlyAppendsAfterTheExactCore(t *testing.T) {
	corpus, c, _ := recordingClient(t)
	const question = "what did Dima say about the retest last week?"
	base, err := c.SearchMemoryPageContext(context.Background(), question)
	if err != nil {
		t.Fatal(err)
	}
	baseLabels, baseRelaxed := pageLabels(corpus, base)
	if len(baseLabels) < 2 || baseLabels[0] != "slack-dima-retest" || slices.Contains(baseLabels, "terminal-venice") {
		t.Fatalf("baseline core = %v", baseLabels)
	}

	stub := withMeaningStub(t, corpus, c)
	page, err := c.SearchMemoryPageContext(context.Background(), question)
	if err != nil {
		t.Fatal(err)
	}
	labels, relaxed := pageLabels(corpus, page)
	if stub.queries.Load() == 0 {
		t.Fatal("a natural-language question with room on the page should have used the meaning step")
	}
	if len(labels) != len(baseLabels)+1 || !slices.Equal(labels[:len(baseLabels)], baseLabels) || !slices.Equal(relaxed[:len(baseLabels)], baseRelaxed) {
		t.Fatalf("core rows or their relaxed flags changed:\n core %v %v\n with %v %v", baseLabels, baseRelaxed, labels, relaxed)
	}
	last := page.Hits[len(page.Hits)-1]
	if corpus.label(last.SourceID) != "terminal-venice" || !last.Relaxed || !last.Semantic || !page.Relaxed {
		t.Fatalf("the meaning match must be appended last and flagged: %s relaxed=%t semantic=%t page.relaxed=%t", corpus.label(last.SourceID), last.Relaxed, last.Semantic, page.Relaxed)
	}
	for _, hit := range page.Hits[:len(baseLabels)] {
		if hit.Semantic {
			t.Fatalf("core row %s was marked semantic", corpus.label(hit.SourceID))
		}
	}
	if len(page.Summaries) != len(base.Summaries) {
		t.Fatalf("summary rows changed with the meaning step: %d vs %d", len(page.Summaries), len(base.Summaries))
	}
	for i := range page.Summaries {
		if page.Summaries[i].ID != base.Summaries[i].ID {
			t.Fatalf("summary rows were reordered by the meaning step: %v vs %v", page.Summaries, base.Summaries)
		}
	}
}

func TestSearchMeaningStepFillsAnEmptyExactPageAndFlagsIt(t *testing.T) {
	corpus, c, _ := recordingClient(t)
	stub := withMeaningStub(t, corpus, c)
	page, err := c.SearchMemoryPageContext(context.Background(), "xyzzyplugh last week")
	if err != nil {
		t.Fatal(err)
	}
	labels, relaxed := pageLabels(corpus, page)
	if stub.queries.Load() == 0 || !slices.Equal(labels, []string{"terminal-venice"}) || !relaxed[0] || !page.Hits[0].Semantic {
		t.Fatalf("an empty exact page may fall back to meaning: embeddings=%d %v %v", stub.queries.Load(), labels, relaxed)
	}
}

func TestSearchMeaningStepIsSkippedForLiteralQueriesAndFullPages(t *testing.T) {
	corpus, c, _ := recordingClient(t)
	stub := withMeaningStub(t, corpus, c)
	if _, err := c.SearchMemoryPageContext(context.Background(), `"xyzzyplugh"`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.searchMemoryPageContext(context.Background(), "what did Dima say about the retest?", 2); err != nil {
		t.Fatal(err)
	}
	if stub.queries.Load() != 0 {
		t.Fatalf("a quoted query and a full page must not reach the meaning step: %d", stub.queries.Load())
	}
}

func TestClosenessReordersOnlyRowsThePageAlreadyHydrated(t *testing.T) {
	_, c, recorder := recordingClient(t)
	var reads atomic.Int64
	recorder.onRead = func() { reads.Add(1) }
	page, err := c.searchMemoryPageContext(context.Background(), "dima retest", 5)
	if err != nil || len(page.Hits) == 0 {
		t.Fatalf("hits=%d err=%v", len(page.Hits), err)
	}
	if got, want := int(reads.Load()), len(page.Hits); got != want {
		t.Fatalf("the closeness tie-break must not read sources of its own: %d source reads for %d hydrated rows", got, want)
	}
}

func TestReorderByClosenessIsATieBreakInsideEachGroup(t *testing.T) {
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	hit := func(id, text string, mask uint32, age time.Duration) SearchHit {
		return SearchHit{SourceID: id, Text: text, matchedMask: mask, CapturedAt: now.Add(-age)}
	}
	hits := []SearchHit{
		hit("scattered-new", "dima "+strings.Repeat("filler ", 40)+"retest", 0b11, 0),
		hit("tight-old", "dima retest", 0b11, 24*time.Hour),
		hit("tight-partial", "dima", 0b01, 0),
	}
	page := naturalPage{relaxed: map[string]bool{"tight-partial": true}, rankers: map[bool]lookupRankInput{false: {}, true: {}}}
	reorderByCloseness(hits, page, []string{"dima", "retest"})
	got := []string{hits[0].SourceID, hits[1].SourceID, hits[2].SourceID}
	if !slices.Equal(got, []string{"tight-old", "scattered-new", "tight-partial"}) {
		t.Fatalf("order = %v: closeness breaks ties inside a group and never moves rows across groups", got)
	}
}

func TestDefaultSummaryScopeCountsThirtyLocalDaysInTheChronicleZone(t *testing.T) {
	corpus, c, _ := recordingClient(t)
	// The window starts 30 local days before 2026-11-16 15:30 Brussels, i.e.
	// 2026-10-17 15:30 CEST = 13:30Z; the last 720 absolute hours would start
	// at 14:30Z, an hour later, across the end-of-October clock change.
	hour := time.Date(2026, 10, 17, 13, 0, 0, 0, time.UTC)
	observation, err := capture.SanitizeObservation("com.apple.Terminal", "", "zebracorn migration notes", capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := corpus.store.PutObservationAt(context.Background(), observation, hour.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	summary := chronicle.Summary{HourID: "hour:" + hour.Format("20060102T15Z"), Start: hour, End: hour.Add(time.Hour), Text: "- Wrote the zebracorn migration notes", SourceIDs: []string{observation.ContentID()}}
	if err := corpus.store.PutChronicle(context.Background(), summary); err != nil {
		t.Fatal(err)
	}
	page, err := c.SearchMemoryPageContext(context.Background(), "zebracorn")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range page.Summaries {
		ids = append(ids, s.ID)
	}
	if !slices.Equal(ids, []string{"chronicle:" + summary.HourID}) {
		t.Fatalf("a recap inside 30 local days must be found: %v", ids)
	}
}
