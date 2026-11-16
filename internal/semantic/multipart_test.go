package semantic_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

func semanticCapture(t *testing.T, store *durable.Store, text string, index bool) capture.CaptureSummary {
	t.Helper()
	return semanticCaptureAt(t, store, text, index, time.Now().UTC())
}

func semanticCaptureAt(t *testing.T, store *durable.Store, text string, index bool, at time.Time) capture.CaptureSummary {
	t.Helper()
	ctx := context.Background()
	builder, err := capture.NewCaptureBuilder("reader.app", "work.example", strings.Repeat("a", 64), at)
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
	if index {
		drainSemanticCapture(t, store)
	}
	return summary
}

func TestServicePublishedPartRevisitUsesActualSightingWithoutReembedding(t *testing.T) {
	ctx := context.Background()
	store := serviceStore(t)
	first := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	visit := first.Add(48 * time.Hour)
	const text = "A reservation for the Atlas project dinner."
	edition := semanticCaptureAt(t, store, text, true, first)
	page, err := store.ListCaptureEvidencePage(ctx, storage.CaptureEvidencePageRequest{Limit: 4, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Sources) != 1 {
		t.Fatal(err)
	}
	e := &serviceEmbedder{}
	s := newService(t, store, e, semantic.Options{})
	if err := s.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	revisited := semanticCaptureAt(t, store, text, true, visit)
	if revisited.ContentID() != edition.ContentID() {
		t.Fatal("unchanged synthetic page changed edition identity")
	}
	hits, err := s.Search(ctx, "dinner reservation", semantic.Query{Limit: 1, Start: visit.Add(-time.Minute), End: visit.Add(time.Minute)})
	if err != nil || len(hits) != 1 || hits[0].SourceID != page.Sources[0].SourceID() || !hits[0].CapturedAt.Equal(visit) {
		t.Fatal("part revisit was lost from time-scoped semantic retrieval", err)
	}
	if e.documents.Load() != 1 {
		t.Fatal("revisit reembedded unchanged published text")
	}
	hits, err = s.Search(ctx, "dinner reservation", semantic.Query{Limit: 1, Start: visit.Add(time.Minute), End: visit.Add(2 * time.Minute)})
	if err != nil || len(hits) != 0 {
		t.Fatal("unseen time window leaked a published part", err)
	}
}

func drainSemanticCapture(t *testing.T, store *durable.Store) {
	t.Helper()
	for range 128 {
		more, err := store.IndexPendingCaptures(context.Background(), 16)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			return
		}
	}
	t.Fatal("capture index did not complete bounded synthetic fixture")
}

func TestServiceIndexesPublishedPartsWithRealIdentityAndReloadsVectors(t *testing.T) {
	ctx := context.Background()
	store := serviceStore(t)
	edition := semanticCapture(t, store, "A reservation for two at the Atlas project dinner.", true)
	page, err := store.ListCaptureEvidencePage(ctx, storage.CaptureEvidencePageRequest{Limit: 4, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Sources) != 1 {
		t.Fatal(err)
	}
	source := page.Sources[0]
	e := &serviceEmbedder{}
	s := newService(t, store, e, semantic.Options{})
	if err := s.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Status().IndexedSources != 1 || e.documents.Load() != 1 {
		t.Fatal("semantic directory omitted published capture part")
	}
	document, found, err := store.GetSemanticDocument(ctx, source.SourceID())
	if err != nil || !found || document.SourceID == edition.ContentID() || document.SourceID != source.SourceID() || !document.CapturedAt.Equal(page.CapturedAt[0]) {
		t.Fatal("semantic part provenance did not survive persisted read", err)
	}
	hits, err := s.Search(ctx, "dinner reservation", semantic.Query{Limit: 1})
	if err != nil || len(hits) != 1 || hits[0].SourceID != source.SourceID() {
		t.Fatal("semantic part did not become queryable", err)
	}
	s.Close()
	e = &serviceEmbedder{}
	s = newService(t, store, e, semantic.Options{})
	if err := s.Backfill(ctx); err != nil {
		t.Fatal("published vectors could not reload", err)
	}
	if e.documents.Load() != 0 || s.Status().IndexedSources != 1 {
		t.Fatal("reopening reembedded or dropped persisted part vectors")
	}
}

func TestServiceWaitsForPartIndexAndOldCheckpointDoesNotHideCaptures(t *testing.T) {
	ctx := context.Background()
	store := serviceStore(t)
	semanticCapture(t, store, "A saved reservation awaits its published part index.", false)
	e := &serviceEmbedder{}
	identity, _ := e.Identity(ctx)
	// This is exactly the old checkpoint shape, with no multipart fields.
	if err := store.PutSemanticCheckpoint(ctx, semantic.Checkpoint{ModelKey: identity.Key(), Complete: true}); err != nil {
		t.Fatal(err)
	}
	s := newService(t, store, e, semantic.Options{})
	if err := s.Backfill(ctx); err == nil {
		t.Fatal("pending published source was called complete")
	}
	if status := s.Status(); status.Complete || status.ErrorCode != "" || e.documents.Load() != 0 {
		t.Fatal("pending index caused inference or was reported as provider failure")
	}
	drainSemanticCapture(t, store)
	if err := s.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	if !s.Status().Complete || s.Status().IndexedSources != 1 || e.documents.Load() != 1 {
		t.Fatal("old checkpoint hid published source after index completion")
	}
}

func TestServiceIncrementalPublishedPartDoesNotRescanLegacyDirectory(t *testing.T) {
	ctx := context.Background()
	store := serviceStore(t)
	probe := &sourcePageProbe{Store: store}
	e := &serviceEmbedder{}
	s, err := semantic.NewService(probe, store, e, semantic.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Start()
	waitServiceReady(t, s, 0)
	pages := probe.pages.Load()
	semanticCapture(t, store, "A new reservation arrived as a canonical capture part.", true)
	page, err := store.ListCaptureEvidencePage(ctx, storage.CaptureEvidencePageRequest{Limit: 4, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Sources) != 1 {
		t.Fatal(err)
	}
	s.NotifySource(page.Sources[0].SourceID())
	waitServiceReady(t, s, 1)
	if probe.pages.Load() != pages || e.documents.Load() != 1 {
		t.Fatal("part notification was lost or triggered legacy directory rescan")
	}
}

func TestServicePartInvalidationRejectsStubbornLateEmbedding(t *testing.T) {
	ctx := context.Background()
	store := serviceStore(t)
	semanticCapture(t, store, "A reservation for the Atlas project dinner.", true)
	page, err := store.ListCaptureEvidencePage(ctx, storage.CaptureEvidencePageRequest{Limit: 4, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Sources) != 1 {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	e := &serviceEmbedder{before: func(context.Context, int32) error { close(entered); <-release; return nil }}
	s := newService(t, store, e, semantic.Options{})
	done := make(chan error, 1)
	go func() { done <- s.Backfill(ctx) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("part embedding did not start")
	}
	s.Invalidate()
	if _, err := store.DeleteCapturesBefore(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("late part embedding ignored invalidation", err)
	}
	if _, found, err := store.GetSemanticDocument(ctx, page.Sources[0].SourceID()); err != nil || found {
		t.Fatal("late embedding resurrected erased part vectors", err)
	}
	if hits, err := s.Search(ctx, "reservation", semantic.Query{Limit: 1}); !errors.Is(err, semantic.ErrUnavailable) || len(hits) != 0 {
		t.Fatal("old part vector remained queryable", err)
	}
}

type changedSemanticCaptureSources struct {
	*durable.Store
	changed bool
}

func (s *changedSemanticCaptureSources) ListCaptureEvidencePage(ctx context.Context, request storage.CaptureEvidencePageRequest) (storage.CaptureEvidencePage, error) {
	if !s.changed {
		s.changed = true
		return storage.CaptureEvidencePage{}, storage.ErrCaptureCursorChanged
	}
	return s.Store.ListCaptureEvidencePage(ctx, request)
}

func TestServiceChangedCaptureCursorDoesNotDiscardLegacyProgress(t *testing.T) {
	ctx := context.Background()
	store := serviceStore(t)
	legacy := serviceSource(t, store, "editor.app", "Legacy compiler source is already covered.")
	semanticCapture(t, store, "A published reservation remains after cursor replacement.", true)
	e := &serviceEmbedder{}
	identity, _ := e.Identity(ctx)
	checkpoint := semantic.Checkpoint{ModelKey: identity.Key(), After: legacy, Complete: true, CaptureAfter: strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64) + ":0"}
	if err := store.PutSemanticCheckpoint(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	s, err := semantic.NewService(&changedSemanticCaptureSources{Store: store}, store, e, semantic.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Backfill(ctx); err == nil {
		t.Fatal("replaced capture cursor was falsely marked covered")
	}
	got, _, err := store.GetSemanticCheckpoint(ctx, identity.Key())
	if err != nil || got.After != legacy || !got.Complete || got.CaptureAfter != "" || got.CaptureComplete {
		t.Fatal("capture reset discarded legacy directory progress", err)
	}
	if err := s.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	if e.documents.Load() != 1 || s.Status().IndexedSources != 1 {
		t.Fatal("capture cursor recovery repeated legacy work or lost published source")
	}
}
