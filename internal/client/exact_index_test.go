package client

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage/durable"
)

func exactLargeFixture(t *testing.T, words int) capture.SanitizedObservation {
	t.Helper()
	var text strings.Builder
	for n := 0; n < words; n++ {
		fmt.Fprintf(&text, "word%06d ", n)
	}
	text.WriteString("zzztailneedle")
	o, err := capture.SanitizeObservation("browser.app", "docs.example", text.String(), capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestExactIndexingRunsWithoutAIAndResumesOnOpen(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "Blur")
	secrets := &bootstrapSecrets{values: make(map[string][]byte)}
	app, err := openLocalAppAt(ctx, directory, secrets, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Explicitly pause both default AI services to prove exact-index recovery
	// has no inference dependency.
	if err := app.Client.ConfigureLocalAI(ctx, LocalAISettings{}); err != nil {
		t.Fatal(err)
	}
	settings := app.Client.MemoryIndexSettings()
	settings.Enabled = false
	if err := app.Client.ConfigureMemoryIndex(ctx, settings); err != nil {
		t.Fatal(err)
	}
	app.exactIndex.Close() // leave durable work for the next process
	o := exactLargeFixture(t, 3000)
	if err := app.store.PutObservation(ctx, o); err != nil {
		t.Fatal(err)
	}
	if pending, err := app.Client.ExactIndexPending(ctx); err != nil || !pending {
		t.Fatal(pending, err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = openLocalAppAt(ctx, directory, secrets, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(app.Close)
	if app.Client.LocalAISettings().Enabled || app.Client.MemoryIndexSettings().Enabled {
		t.Fatal("fixture unexpectedly requires a model")
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		pending, err := app.Client.ExactIndexPending(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("model-free exact worker failed to resume")
		}
		time.Sleep(10 * time.Millisecond)
	}
	page, err := app.Client.SearchMemoryPageContext(ctx, "zzztailneedle")
	if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != o.ContentID() || page.IndexingPending {
		t.Fatal(page, err)
	}
}

func TestMemorySearchAndAskDisclosePendingExactCoverage(t *testing.T) {
	ctx := context.Background()
	// Stop the fixture's worker so it cannot race coverage assertions.
	// No real keychain, settings or app store is touched.
	directory := filepath.Join(t.TempDir(), "Blur")
	app, err := openLocalAppAt(ctx, directory, &bootstrapSecrets{values: make(map[string][]byte)}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(app.Close)
	app.exactIndex.Close()
	o := exactLargeFixture(t, 400)
	if err := app.store.PutObservation(ctx, o); err != nil {
		t.Fatal(err)
	}
	page, err := app.Client.SearchMemoryPageContext(ctx, "zzztailneedle")
	if err != nil || len(page.Hits) != 0 || !page.IndexingPending {
		t.Fatal("search hid incomplete coverage", page, err)
	}
	retrieval, err := app.Client.RetrievePlan(ctx, assistant.Plan{Kind: assistant.KindLookup, Question: "What did I read about zzztailneedle?", Terms: []string{"zzztailneedle"}})
	if err != nil || !retrieval.Diagnosis.IndexingPending {
		t.Fatal("Ask hid incomplete coverage", retrieval, err)
	}
	response, err := search.New(app.store).Search(ctx, search.Request{Query: "zzztailneedle", ResultLimit: 4, SnippetBytes: 64, ResponseBytes: 4096, ScanLimit: 1})
	if err != nil || !response.IndexingPending {
		t.Fatal("MCP search hid incomplete coverage", response, err)
	}
	mcp, err := app.Client.HandleMCPRead(ctx, "search_memory", json.RawMessage(`{"query":"zzztailneedle","limit":3}`))
	var mcpResponse mcpSearchResponse
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mcp, &mcpResponse); err != nil || !mcpResponse.IndexingPending {
		t.Fatal("native MCP omitted pending coverage", string(mcp), err)
	}
	turns := appendQuickConversation(nil, "find zzztailneedle", assistant.Result{IndexingPending: true, Answer: assistant.Answer{Text: "Some matching evidence."}})
	if len(turns) != 1 || !strings.Contains(quickTurnMeta(turns[0]), "still indexing") {
		t.Fatal("quick answer lost coverage", turns)
	}
	if more, err := app.store.IndexPendingExact(ctx, 100); err != nil || more {
		t.Fatal(more, err)
	}
	page, err = app.Client.SearchMemoryPageContext(ctx, "zzztailneedle")
	if err != nil || page.IndexingPending || len(page.Hits) != 1 {
		t.Fatal(page, err)
	}
	retrieval, err = app.Client.RetrievePlan(ctx, assistant.Plan{Kind: assistant.KindLookup, Question: "What did I read about zzztailneedle?", Terms: []string{"zzztailneedle"}})
	if err != nil || len(retrieval.Evidence) != 1 {
		t.Fatal(retrieval, err)
	}
	ev := retrieval.Evidence[0]
	if ev.MatchStart <= 0 || ev.MatchEnd <= ev.MatchStart || !strings.Contains(ev.Text[ev.MatchStart:ev.MatchEnd], "zzztailneedle") {
		t.Fatal("answer context missed the match at the end of the page", ev.MatchStart, ev.MatchEnd)
	}
	if err := app.store.DeleteObservationRecord(ctx, o.ContentID()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := app.store.GetObservation(ctx, o.ContentID()); err != nil || found {
		t.Fatal(found, err)
	}
}

var _ exactIndexStore = (*durable.Store)(nil)

type blockedExactIndex struct{ started, finished chan struct{} }

func (b *blockedExactIndex) ExactIndexPending(context.Context) (bool, error) { return true, nil }
func (b *blockedExactIndex) IndexPendingExact(ctx context.Context, _ int) (bool, error) {
	close(b.started)
	<-ctx.Done()
	close(b.finished)
	return true, ctx.Err()
}
func TestExactIndexWorkerCloseCancelsAndDrainsActiveBatch(t *testing.T) {
	store := &blockedExactIndex{started: make(chan struct{}), finished: make(chan struct{})}
	c := newExactIndexController(store, nil, nil)
	<-store.started
	c.Close()
	select {
	case <-store.finished:
	default:
		t.Fatal("worker closed without draining active work")
	}
	c.Close()
}

func TestSeveralLargeMatchesReturnBoundedResultsInsteadOfFailingSearch(t *testing.T) {
	data := &testData{observations: make(map[string]capture.SanitizedObservation)}
	for n := 0; n < 3; n++ {
		text := strings.Repeat("prefix ", 125000) + fmt.Sprintf("marker%d", n)
		o, err := capture.SanitizeObservation("browser.app", "docs.example", text, capture.MaxObservationTextBytes)
		if err != nil {
			t.Fatal(err)
		}
		data.observations[o.ContentID()] = o
		data.page.Hits = append(data.page.Hits, search.IndexHit{SourceID: o.ContentID(), Score: 1, Bytes: len(o.Text()) + len(o.AppID()) + len(o.Domain())})
	}
	data.page.Rows = len(data.page.Hits)
	c := NewClient(data)
	defer c.Close()
	page, err := c.SearchMemoryPageContext(context.Background(), "prefix")
	if err != nil || len(page.Hits) != 2 || !page.HasMore {
		t.Fatal("aggregate budget rejected valid matches", len(page.Hits), page.HasMore, err)
	}
	raw, err := c.HandleMCPRead(context.Background(), "search_memory", json.RawMessage(`{"query":"prefix","limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	var mcpResponse mcpSearchResponse
	if err := json.Unmarshal(raw, &mcpResponse); err != nil || len(mcpResponse.Results) != 2 || !mcpResponse.Truncated {
		t.Fatal("MCP local read budget was not enforced", len(mcpResponse.Results), mcpResponse.Truncated, err)
	}
	for _, hit := range page.Hits {
		if len(hit.Text) < 800000 {
			t.Fatal("source hydration silently cut the original")
		}
	}
}

func TestMCPReturnsRelevantPassageFromLargePage(t *testing.T) {
	ctx := context.Background()
	app, err := openLocalAppAt(ctx, filepath.Join(t.TempDir(), "Blur"), &bootstrapSecrets{values: make(map[string][]byte)}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(app.Close)
	app.exactIndex.Close()
	o := exactLargeFixture(t, 20000)
	if err := app.store.PutObservation(ctx, o); err != nil {
		t.Fatal(err)
	}
	if more, err := app.store.IndexPendingExact(ctx, 400); err != nil || more {
		t.Fatal(more, err)
	}
	raw, err := app.Client.HandleMCPRead(ctx, "search_memory", json.RawMessage(`{"query":"zzztailneedle","limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	var response mcpSearchResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || !strings.Contains(response.Results[0].Text, "zzztailneedle") || len(response.Results[0].Text) > maxMCPReadTextBytes || !response.Results[0].Truncated {
		t.Fatal("MCP dropped the large source or its tail passage", response)
	}
}

func TestPartialSavedSourceKeepsCoverageThroughSearchAskMCPAndInspection(t *testing.T) {
	ctx := context.Background()
	o, err := capture.SanitizeObservation("editor.app", "local", "Visible release note\npassword:x", len("Visible release note\npassword:x"))
	if err != nil || !o.Truncated() {
		t.Fatal("invalid partial fixture", err)
	}
	data := &testData{observations: map[string]capture.SanitizedObservation{o.ContentID(): o}, page: search.ExactPage{Hits: []search.IndexHit{{SourceID: o.ContentID(), Score: 1, Bytes: len(o.Text()) + len(o.AppID()) + len(o.Domain())}}, Rows: 1}}
	c := NewClient(data)
	defer c.Close()
	page, err := c.SearchMemoryPageContext(ctx, "release")
	if err != nil || len(page.Hits) != 1 || !page.Hits[0].Partial {
		t.Fatal("Search lost partial-source provenance", page, err)
	}
	retrieval, err := c.RetrievePlan(ctx, assistant.Plan{Kind: assistant.KindLookup, Question: "What was the release note?", Terms: []string{"release"}})
	if err != nil || len(retrieval.Evidence) != 1 || !retrieval.Evidence[0].Partial || !strings.Contains(askEvidenceCaption(retrieval.Evidence[0]), "Only part") {
		t.Fatal("Ask lost partial-source provenance", retrieval, err)
	}
	raw, err := c.HandleMCPRead(ctx, "search_memory", json.RawMessage(`{"query":"release","limit":2}`))
	var mcp mcpSearchResponse
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &mcp); err != nil || len(mcp.Results) != 1 || !mcp.Results[0].Partial {
		t.Fatal("MCP lost partial-source provenance", string(raw), err)
	}
	source, err := c.ChronicleSource(ctx, o.ContentID())
	if err != nil || !source.Partial || source.Text != o.Text() {
		t.Fatal("source inspection hid incomplete capture", source, err)
	}
}
