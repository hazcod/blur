package durable

import (
	"context"
	"testing"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
)

func titledIndexObservation(t *testing.T, app, domain, text, title string) capture.SanitizedObservation {
	t.Helper()
	return indexObservation(t, app, domain, text).WithTitle(title)
}

func exactTerm(t *testing.T, store *Store, term string) search.ExactPage {
	t.Helper()
	page, err := store.SearchExact(context.Background(), search.ExactQuery{Terms: []string{term}, MaxRows: 64, MaxBytes: search.MaxIndexBytes})
	if err != nil {
		t.Fatalf("SearchExact(%q): %v", term, err)
	}
	return page
}

func TestDurableIndexSearchesWindowTitleWithWeightTwo(t *testing.T) {
	store, err := OpenDurable(t.TempDir()+"/title.db", durableTestKey(t, 0x61), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	titled := titledIndexObservation(t, "com.tinyspeck.slackmacgap", "app.slack.com", "sidebar messages", "Dima, Dziga (DM) - Cresco - Slack")
	textOnly := indexObservation(t, "notes.app", "docs.example", "cresco quarterly planning")
	for _, o := range []capture.SanitizedObservation{titled, textOnly} {
		if err := store.PutObservation(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.IndexPendingExact(context.Background(), 32); err != nil {
		t.Fatal(err)
	}

	// "dziga" is only in the title: weight 2.
	page := exactTerm(t, store, "dziga")
	if len(page.Hits) != 1 || page.Hits[0].SourceID != titled.ContentID() || page.Hits[0].Score != 2 {
		t.Fatalf("title-only term hits = %+v, want one hit scoring 2", page.Hits)
	}
	// "cresco" is in one title (2) and one body (1): the title ranks first.
	page = exactTerm(t, store, "cresco")
	if len(page.Hits) != 2 || page.Hits[0].SourceID != titled.ContentID() || page.Hits[0].Score != 2 || page.Hits[1].SourceID != textOnly.ContentID() || page.Hits[1].Score != 1 {
		t.Fatalf("title vs body hits = %+v", page.Hits)
	}
	// A term in both the app (3) and the title (2) keeps the higher weight.
	both := titledIndexObservation(t, "pocket", "example.com", "body", "Pocket notes")
	if err := store.PutObservation(context.Background(), both); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IndexPendingExact(context.Background(), 32); err != nil {
		t.Fatal(err)
	}
	if page = exactTerm(t, store, "pocket"); len(page.Hits) != 1 || page.Hits[0].Score != 3 {
		t.Fatalf("app+title term hits = %+v, want score 3", page.Hits)
	}
	// All query terms must still be present across fields (title + body).
	multi, err := store.SearchExact(context.Background(), search.ExactQuery{Terms: []string{"cresco", "messages"}, MaxRows: 64, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(multi.Hits) != 1 || multi.Hits[0].SourceID != titled.ContentID() {
		t.Fatalf("title+body terms = %+v err=%v", multi.Hits, err)
	}
}

func TestDurableIndexTitleReachesEngineSearchAndFoldsDiacritics(t *testing.T) {
	store, err := OpenDurable(t.TempDir()+"/title-engine.db", durableTestKey(t, 0x62), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	o := titledIndexObservation(t, "com.apple.preview", "", "Experience summary mentions his résumé and Dima's references.", "CV_Youssef_résumé.pdf")
	if err := store.PutObservation(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IndexPendingExact(context.Background(), 32); err != nil {
		t.Fatal(err)
	}
	engine := search.New(store)
	for _, query := range []string{"resume", "résumé", "RÉSUMÉ", "youssef", "dima", "Dima's"} {
		response, err := engine.Search(context.Background(), search.Request{Query: query, ResultLimit: 5, SnippetBytes: 64, ResponseBytes: 8192, ScanLimit: 1})
		if err != nil || len(response.Results) != 1 || response.Results[0].SourceID != o.ContentID() {
			t.Errorf("query %q: results=%+v err=%v", query, response.Results, err)
		}
	}
	// A title-only term scores as a title match through the engine as well
	// (the engine recomputes it from the stored evidence and compares).
	response, err := engine.Search(context.Background(), search.Request{Query: "pdf", ResultLimit: 5, SnippetBytes: 64, ResponseBytes: 8192, ScanLimit: 1})
	if err != nil || len(response.Results) != 1 || response.Results[0].Score != 2 {
		t.Fatalf("title-only query: %+v err=%v", response.Results, err)
	}
}

func TestDurableIndexTitleSurvivesReopen(t *testing.T) {
	path := t.TempDir() + "/title-reopen.db"
	key := durableTestKey(t, 0x63)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	o := titledIndexObservation(t, "notion.id", "notion.so", "page body", "Pentest planning Q4")
	if err := store.PutObservation(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IndexPendingExact(context.Background(), 32); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(reopened.Close)
	saved, found, err := reopened.GetObservation(context.Background(), o.ContentID())
	if err != nil || !found || saved.Title() != "Pentest planning Q4" {
		t.Fatalf("title after reopen = %q found=%t err=%v", saved.Title(), found, err)
	}
	if page := exactTerm(t, reopened, "pentest"); len(page.Hits) != 1 || page.IndexingPending {
		t.Fatalf("title term after a plain reopen: %+v", page)
	}
}

// A store whose index was built before titles and folding existed carries the
// previous format marker. Opening it must return promptly with the work queued
// (pending), and the background indexer must then make titles and folded
// tokens searchable, with no data lost or additional schema transition.
func TestDurableIndexFormatBumpRebuildsThroughPendingBackfill(t *testing.T) {
	path := t.TempDir() + "/previous-format.db"
	key := durableTestKey(t, 0x64)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	slack := titledIndexObservation(t, "com.tinyspeck.slackmacgap", "app.slack.com", "sidebar and messages", "Dima, Dziga (DM) - Cresco - Slack")
	preview := titledIndexObservation(t, "com.apple.preview", "", "his résumé, attached", "CV_Youssef_résumé.pdf")
	plain := indexObservation(t, "terminal.app", "", "plain old capture without a title")
	for _, o := range []capture.SanitizedObservation{slack, preview, plain} {
		if err := store.PutObservation(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	// Rewrite the ready marker as the previous release wrote it.
	previous, err := store.seal(searchIndexStateKey, "search-index-state-v1", []byte{searchIndexFormatVersion - 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return tx.writeEntryLocked(searchIndexStateKey, previous, true)
	}); err != nil {
		t.Fatal(err)
	}
	clear(previous)
	schemaBefore := store.state.schemaVer
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatalf("a store written at the previous index format must open: %v", err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	// Rebuilding the index preserves the current authenticated reader floor.
	if store.state.schemaVer != schemaBefore || schemaBefore != OpenSchemaVersion {
		t.Fatalf("index rebuild changed schema: before=%d after=%d current=%d", schemaBefore, store.state.schemaVer, OpenSchemaVersion)
	}
	if pending, err := store.ExactIndexPending(ctx); err != nil || !pending {
		t.Fatalf("after open the rebuild must be pending: pending=%t err=%v", pending, err)
	}
	page, err := store.SearchExact(ctx, search.ExactQuery{Terms: []string{"dziga"}, MaxRows: 64, MaxBytes: search.MaxIndexBytes})
	if err != nil || !page.IndexingPending {
		t.Fatalf("a search during the rebuild must report IndexingPending: %+v err=%v", page, err)
	}
	for rounds := 0; ; rounds++ {
		more, err := store.IndexPendingExact(ctx, 32)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
		if rounds > 1000 {
			t.Fatal("rebuild did not finish")
		}
	}
	if pending, err := store.ExactIndexPending(ctx); err != nil || pending {
		t.Fatalf("rebuild finished but still pending: %t %v", pending, err)
	}
	if page := exactTerm(t, store, "dziga"); len(page.Hits) != 1 || page.Hits[0].SourceID != slack.ContentID() || page.Hits[0].Score != 2 || page.IndexingPending {
		t.Fatalf("title term after rebuild: %+v", page)
	}
	if page := exactTerm(t, store, "resume"); len(page.Hits) != 1 || page.Hits[0].SourceID != preview.ContentID() {
		t.Fatalf("folded term after rebuild: %+v", page)
	}
	if page := exactTerm(t, store, "plain"); len(page.Hits) != 1 || page.Hits[0].SourceID != plain.ContentID() {
		t.Fatalf("a capture without a title was lost in the rebuild: %+v", page)
	}
	// The rebuilt store is stable: another open does not rebuild again.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(again.Close)
	if pending, err := again.ExactIndexPending(ctx); err != nil || pending {
		t.Fatalf("second open pending=%t err=%v", pending, err)
	}
}

// A store whose capture-part index was marked at the previous content version
// keeps opening while bounded background migration updates derived tokens.
func TestCapturePartIndexMarkerFromPreviousFormatStillOpens(t *testing.T) {
	path := t.TempDir() + "/capture-part-marker.db"
	key := durableTestKey(t, 0x65)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	index := store.capturePartIndex()
	previous, err := index.seal(index.exactStateKey(), index.exactNamespace("search-index-state-v1"), []byte{searchIndexStructureVersion, 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return tx.writeEntryLocked(index.exactStateKey(), previous, true)
	}); err != nil {
		t.Fatal(err)
	}
	clear(previous)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatalf("capture-part marker at the previous format must open: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
