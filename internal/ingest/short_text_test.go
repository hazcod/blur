package ingest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

// A length floor must not silently discard commands or statuses before the
// encrypted sink, or lose them when the next foreground capture arrives.
func TestShortTerminalCapturesPersistContinueAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "short-terminal.db")
	store, err := durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var current capture.SanitizedObservation
	source := &eventSource{probe: platform.CaptureProbe{TargetID: "terminal", AppID: "com.apple.terminal"}}
	service, err := newService(source, captureFunc(func(context.Context) (capture.SanitizedObservation, error) { return current, nil }), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"ls", "ok", "0", "✓", "日", "e\u0301", "  pwd \n"}
	ids := make([]string, 0, len(texts))
	for _, text := range texts {
		current, err = capture.SanitizeObservation("com.apple.terminal", "", text, 256)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, current.ContentID())
		if err := service.captureCurrent(ctx); err != nil {
			t.Fatal(err)
		}
		got, found, err := store.GetObservation(ctx, current.ContentID())
		if err != nil || !found || got.Text() != text {
			t.Fatalf("short terminal text %q dropped before encrypted persistence: found=%v text=%q stats=%+v err=%v", text, found, got.Text(), service.Stats(), err)
		}
	}
	if err := service.captureCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	if stats := service.Stats(); stats.Persisted != uint64(len(texts)) || stats.Duplicates != 1 || stats.DroppedShort != 0 {
		t.Fatalf("short capture continuation stats=%+v", stats)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		evidence, found, err := store.GetSourceEvidence(ctx, id)
		if err != nil || !found || evidence.Text() != texts[i] || evidence.AppID() != "com.apple.terminal" {
			t.Fatalf("short source %d lost on reopen: found=%v text=%q err=%v", i, found, evidence.Text(), err)
		}
	}
	page, err := store.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: 10, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != len(texts) {
		t.Fatalf("short captures duplicated or missing: rows=%d err=%v", len(page.Observations), err)
	}
	hits, err := store.SearchExact(ctx, search.ExactQuery{Terms: []string{"pwd"}, MaxRows: search.MaxIndexRows, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(hits.Hits) != 1 || hits.Hits[0].SourceID != ids[len(ids)-1] {
		t.Fatalf("short command unavailable to exact recall: %+v err=%v", hits, err)
	}
}
