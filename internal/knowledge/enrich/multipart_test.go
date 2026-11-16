package enrich_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

func publishedLibraryCapture(t *testing.T, store *durable.Store, text string, indexed ...bool) capture.CaptureSummary {
	t.Helper()
	ctx := context.Background()
	builder, err := capture.NewCaptureBuilder("editor.app", "work.example", strings.Repeat("a", 64), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer builder.Abort()
	if err := store.BeginCapture(ctx, builder.Header()); err != nil {
		t.Fatal(err)
	}
	sink := func(ctx context.Context, parts []capture.CapturePart) error {
		return store.AppendCaptureParts(ctx, builder.Header(), parts)
	}
	for len(text) > 0 {
		n := min(len(text), 4096)
		if err := builder.Write(ctx, text[:n], sink); err != nil {
			t.Fatal(err)
		}
		text = text[n:]
	}
	summary, err := builder.Finish(ctx, capture.CaptureComplete, sink)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	if len(indexed) > 0 && !indexed[0] {
		return summary
	}
	for range 128 {
		more, err := store.IndexPendingCaptures(ctx, 16)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			return summary
		}
	}
	t.Fatal("synthetic multipart index did not complete bounded work")
	return capture.CaptureSummary{}
}

type changedCaptureCursorSources struct {
	*durable.Store
	changed bool
}

func (s *changedCaptureCursorSources) ListCaptureEvidencePage(ctx context.Context, request storage.CaptureEvidencePageRequest) (storage.CaptureEvidencePage, error) {
	if !s.changed {
		s.changed = true
		return storage.CaptureEvidencePage{}, storage.ErrCaptureCursorChanged
	}
	return s.Store.ListCaptureEvidencePage(ctx, request)
}

func TestLibraryChangedCaptureCursorPreservesLegacyDirectoryProgress(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	legacy := source(t, store, "Legacy Atlas source is already covered.")
	publishedLibraryCapture(t, store, "New published Atlas source remains readable after cursor reset.")
	g := &refusingLibraryGenerator{digest: "generic-source-model", calls: map[string]int{}}
	checkpoint := enrich.Checkpoint{After: legacy.ContentID(), Complete: true, ModelKey: enrich.ModelKey(g.digest), CaptureAfter: strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64) + ":0"}
	if err := store.PutEnrichmentCheckpoint(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	worker, err := enrich.NewService(&changedCaptureCursorSources{Store: store}, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if err := worker.Backfill(ctx); !errors.Is(err, enrich.ErrMoreWork) {
		t.Fatal("capture cursor reset became provider failure", err)
	}
	saved, _, err := store.GetEnrichmentCheckpoint(ctx)
	if err != nil || saved.After != checkpoint.After || !saved.Complete || saved.CaptureAfter != "" || saved.CaptureComplete {
		t.Fatal("capture cursor reset discarded legacy directory progress", err)
	}
	complete(t, worker)
	if len(g.calls) != 1 || g.calls[legacy.ContentID()] != 0 {
		t.Fatal("capture retry repeated legacy model work or lost published source")
	}
}

func TestLibraryDeclinedPartDoesNotBlockOtherPublishedParts(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	publishedLibraryCapture(t, store, strings.Repeat("Atlas project has an offline export workflow.\n", 800)+"The Atlas final tail is retained.")
	page, err := store.ListCaptureEvidencePage(ctx, storage.CaptureEvidencePageRequest{Limit: 4, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Sources) != 2 {
		t.Fatal("fixture did not expose two canonical source parts", err)
	}
	first, last := page.Sources[0].SourceID(), page.Sources[1].SourceID()
	g := &refusingLibraryGenerator{digest: "generic-source-model", refuseID: first, calls: map[string]int{}}
	worker, err := enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	complete(t, worker)
	if !worker.Status().CoverageLimited || g.calls[first] != 1 || g.calls[last] == 0 {
		t.Fatal("one declined source part blocked the rest of the published capture")
	}
	if progress, found, err := store.GetEnrichmentProgress(ctx, first); err != nil || !found || !progress.Declined || progress.Complete || progress.Next != 0 {
		t.Fatal("part refusal falsely claimed coverage", err)
	}
	if progress, found, err := store.GetEnrichmentProgress(ctx, last); err != nil || !found || !progress.Complete || progress.Declined {
		t.Fatal("later source part was not processed", err)
	}
	for _, expected := range page.Sources {
		actual, found, err := store.GetSourceEvidence(ctx, expected.SourceID())
		if err != nil || !found || actual.Text() != expected.Text() {
			t.Fatal("source transformation refusal changed saved published text", err)
		}
	}
}

func TestLibraryWaitsForPublishedPartIndexWithoutClaimingCoverage(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	g := &refusingLibraryGenerator{digest: "generic-source-model", calls: map[string]int{}}
	worker, err := enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	complete(t, worker)
	publishedLibraryCapture(t, store, "New Atlas capture is waiting for its exact index.", false)
	worker.Wake()
	if err := worker.Backfill(context.Background()); !errors.Is(err, enrich.ErrMoreWork) {
		t.Fatal("unindexed saved parts were treated as an empty complete directory", err)
	}
	if status := worker.Status(); status.Complete || len(g.calls) != 0 {
		t.Fatal("Library inferred unreadable parts or falsely completed pending index")
	}
	for range 128 {
		more, err := store.IndexPendingCaptures(context.Background(), 16)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	complete(t, worker)
	if len(g.calls) != 1 || !worker.Status().Complete {
		t.Fatal("resumable published source was lost after index completion")
	}
}

func TestLibraryOldCompletedCheckpointStillDiscoversPublishedParts(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	publishedLibraryCapture(t, store, "Atlas capture exists beside a legacy completed directory checkpoint.")
	g := &refusingLibraryGenerator{digest: "generic-source-model", calls: map[string]int{}}
	if err := store.PutEnrichmentCheckpoint(context.Background(), enrich.Checkpoint{ModelKey: enrich.ModelKey(g.digest), Complete: true}); err != nil {
		t.Fatal(err)
	}
	worker, err := enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	complete(t, worker)
	if len(g.calls) != 1 {
		t.Fatal("legacy complete cursor hid newly supported source directory")
	}
}

func TestLibraryMultipartChunkBudgetResumesWithinPageAfterReopen(t *testing.T) {
	path, key := filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey()
	store, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	edition := publishedLibraryCapture(t, store, strings.Repeat("Atlas project has an offline export workflow.\n", 800)+"The Atlas final tail is retained.")
	if edition.PartCount() < 2 {
		t.Fatal("fixture did not produce multiple parts")
	}
	g := &refusingLibraryGenerator{digest: "generic-source-model", calls: map[string]int{}}
	worker, err := enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Backfill(context.Background()); !errors.Is(err, enrich.ErrMoreWork) {
		t.Fatal("first four chunks claimed full multi-part coverage", err)
	}
	if worker.Status().ProcessedChunks != 4 || len(g.calls) != 1 {
		t.Fatal("source chunk budget was not respected")
	}
	worker.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	worker, err = enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	complete(t, worker)
	if int64(len(g.calls)) != edition.PartCount() {
		t.Fatal("yielding mid-page skipped or relabeled multipart source")
	}
	for id := range g.calls {
		progress, found, err := store.GetEnrichmentProgress(context.Background(), id)
		if err != nil || !found || !progress.Complete {
			t.Fatal("reopened part progress incomplete", err)
		}
	}
	calls := 0
	for _, count := range g.calls {
		calls += count
	}
	worker.Wake()
	complete(t, worker)
	after := 0
	for _, count := range g.calls {
		after += count
	}
	if after != calls {
		t.Fatal("repair repeated already committed multipart chunks")
	}
}

func TestLibraryDirectoryConsumesLegacyAndPublishedPartsAfterRestart(t *testing.T) {
	path, key := filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey()
	store, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	legacy := source(t, store, "Legacy Atlas source mentions offline export.")
	edition := publishedLibraryCapture(t, store, "Published multipart Atlas source mentions offline export.")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	g := &refusingLibraryGenerator{digest: "generic-source-model", calls: map[string]int{}}
	worker, err := enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	complete(t, worker)
	if len(g.calls) != 2 || g.calls[legacy.ContentID()] != 1 {
		t.Fatal("Library directory hid legacy or published multipart evidence")
	}
	for id, calls := range g.calls {
		if id == edition.ContentID() || calls != 1 {
			t.Fatal("Library forged edition-as-observation identity or retried completed source")
		}
		progress, found, err := store.GetEnrichmentProgress(context.Background(), id)
		if err != nil || !found || !progress.Complete {
			t.Fatal("published source part did not persist exact progress", err)
		}
	}
	worker.Wake()
	complete(t, worker)
	for _, calls := range g.calls {
		if calls != 1 {
			t.Fatal("directory repair repeated completed model inference")
		}
	}
}
