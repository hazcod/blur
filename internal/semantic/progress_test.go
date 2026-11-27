package semantic_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// A later timed-out batch must not make an earlier successful batch run again.
// Only the local embedding boundary is substituted; encrypted persistence,
// source validation, reopening and semantic publication are real.
type progressEmbedder struct {
	serviceEmbedder
	passBatches int
	timeout     bool
	embedded    map[string]int
}

func (e *progressEmbedder) EmbedDocuments(_ context.Context, _ semantic.Model, input []string) ([][]float32, error) {
	e.passBatches++
	if e.timeout && e.passBatches > 1 {
		return nil, context.DeadlineExceeded
	}
	result := make([][]float32, len(input))
	for i, text := range input {
		e.embedded[text]++
		result[i] = []float32{1, 0}
	}
	return result, nil
}

func semanticProgressText() string {
	var text strings.Builder
	for i := 0; i < 96; i++ {
		fmt.Fprintf(&text, "Reservation document passage %04d contains distinct durable checkpoint evidence.\n", i)
	}
	return text.String()
}

func TestServiceEmbeddingProgressSurvivesRepeatedBatchDeadlinesAndEncryptedReopen(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "memory")
	if err := bolt.CreatePrivateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "memory.db")
	store, err := durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	id := serviceSource(t, store, "reader.app", semanticProgressText())
	e := &progressEmbedder{timeout: true, embedded: make(map[string]int)}
	complete := false
	for pass := 0; pass < 12; pass++ {
		e.passBatches = 0
		s, err := semantic.NewService(store, store, e, semantic.Options{})
		if err != nil {
			t.Fatal(err)
		}
		err = s.Backfill(ctx)
		if err == nil && s.Status().Complete {
			complete = true
			s.Close()
			break
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if _, found, err := store.GetSemanticDocument(ctx, id); err != nil || found {
			t.Fatalf("unfinished source was published: found=%v err=%v", found, err)
		}
		page, err := store.ListSemanticDocuments(ctx, semantic.PageRequest{Limit: 8, MaxBytes: semantic.MaxPageBytes})
		if err != nil || len(page.Documents) != 0 {
			t.Fatalf("unfinished source entered reload directory: %+v %v", page, err)
		}
		hits, err := s.Search(ctx, "reservation", semantic.Query{Limit: 1})
		if err != nil || len(hits) != 0 {
			t.Fatalf("unfinished source was searchable: %+v %v", hits, err)
		}
		s.Close()
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(id)) || bytes.Contains(raw, []byte("fixture:latest")) {
			t.Fatal("semantic progress metadata was persisted in plaintext")
		}
		clear(raw)
		store, err = durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.OpenExisting)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !complete {
		t.Fatal("repeated later-batch deadlines prevented source completion")
	}
	document, found, err := store.GetSemanticDocument(ctx, id)
	if err != nil || !found || document.Validate() != nil || document.TextBytes != len(semanticProgressText()) {
		t.Fatalf("completed source missing: found=%v err=%v", found, err)
	}
	if len(e.embedded) != len(document.Chunks) {
		t.Fatalf("embedded %d distinct chunks; published %d", len(e.embedded), len(document.Chunks))
	}
	for _, count := range e.embedded {
		if count != 1 {
			t.Fatal("a previously successful embedding batch was repeated")
		}
	}
	model, _ := e.Identity(ctx)
	if _, found, err := store.GetSemanticProgress(ctx, id, model.Key()); err != nil || found {
		t.Fatalf("completed source retained partial progress: found=%v err=%v", found, err)
	}
}

func TestServiceEmbeddingProgressRestartsForNewModel(t *testing.T) {
	ctx := context.Background()
	store := serviceStore(t)
	id := serviceSource(t, store, "reader.app", semanticProgressText())
	e := &progressEmbedder{timeout: true, embedded: make(map[string]int)}
	s, err := semantic.NewService(store, store, e, semantic.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Backfill(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first pass: %v", err)
	}
	oldModel, _ := e.Identity(ctx)
	progress, found, err := store.GetSemanticProgress(ctx, id, oldModel.Key())
	if err != nil || !found || len(progress.Document.Chunks) == 0 {
		t.Fatalf("successful batch missing: found=%v err=%v", found, err)
	}
	oldInput := make(map[string]bool)
	for text := range e.embedded {
		oldInput[text] = true
	}
	e.digest, e.timeout, e.passBatches = "b", false, 0
	newModel, _ := e.Identity(ctx)
	if _, found, err := store.GetSemanticProgress(ctx, id, newModel.Key()); err != nil || found {
		t.Fatalf("new model reused old progress: found=%v err=%v", found, err)
	}
	if err := s.Backfill(ctx); err != nil || !s.Status().Complete {
		t.Fatalf("new model did not complete: %+v %v", s.Status(), err)
	}
	document, found, err := store.GetSemanticDocument(ctx, id)
	if err != nil || !found || document.Model.Key() != newModel.Key() {
		t.Fatalf("wrong model published: found=%v err=%v", found, err)
	}
	for text, count := range e.embedded {
		want := 1
		if oldInput[text] {
			want = 2
		}
		if count != want {
			t.Fatalf("model generation mixed saved embeddings: count=%d want=%d", count, want)
		}
	}
}

func TestServiceEmbeddingProgressCannotResurrectDeletedSource(t *testing.T) {
	ctx := context.Background()
	store := serviceStore(t)
	id := serviceSource(t, store, "reader.app", semanticProgressText())
	e := &progressEmbedder{timeout: true, embedded: make(map[string]int)}
	s, err := semantic.NewService(store, store, e, semantic.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Backfill(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first pass: %v", err)
	}
	model, _ := e.Identity(ctx)
	progress, found, err := store.GetSemanticProgress(ctx, id, model.Key())
	if err != nil || !found {
		t.Fatalf("successful batch missing: found=%v err=%v", found, err)
	}
	s.Invalidate()
	if err := store.DeleteObservationRecord(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetSemanticProgress(ctx, id, model.Key()); err != nil || found {
		t.Fatalf("deleted source retained progress: found=%v err=%v", found, err)
	}
	if err := store.PutSemanticProgress(ctx, progress); !errors.Is(err, semantic.ErrSourceGone) {
		t.Fatalf("late batch resurrected deleted progress: %v", err)
	}
	e.passBatches, e.timeout = 0, false
	for pass := 0; pass < 4 && !s.Status().Complete; pass++ {
		_ = s.Backfill(ctx)
	}
	if !s.Status().Complete || s.Status().IndexedSources != 0 {
		t.Fatalf("erased progress reentered semantic index: %+v", s.Status())
	}
	if e.passBatches != 0 {
		t.Fatal("deleted source was embedded again")
	}
}

type cancelPublishedSemanticRepository struct {
	*durable.Store
	cancel context.CancelFunc
}

func (r *cancelPublishedSemanticRepository) PutSemanticDocument(ctx context.Context, document semantic.Document) error {
	if err := r.Store.PutSemanticDocument(ctx, document); err != nil {
		return err
	}
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	return nil
}

func TestServiceEmbeddingProgressReusesPublicationWhenCacheUpdateWasCanceled(t *testing.T) {
	store := serviceStore(t)
	id := serviceSource(t, store, "reader.app", "Reservation source published just before cancellation.")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repository := &cancelPublishedSemanticRepository{Store: store, cancel: cancel}
	e := &progressEmbedder{embedded: make(map[string]int)}
	s, err := semantic.NewService(store, repository, e, semantic.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Backfill(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("publication cancellation: %v", err)
	}
	if _, found, err := store.GetSemanticDocument(context.Background(), id); err != nil || !found {
		t.Fatalf("complete publication was not committed: found=%v err=%v", found, err)
	}
	if err := s.Backfill(context.Background()); err != nil || !s.Status().Complete {
		t.Fatalf("published source did not resume: %+v %v", s.Status(), err)
	}
	for _, count := range e.embedded {
		if count != 1 {
			t.Fatal("canceled cache update reembedded an already-published source")
		}
	}
}

func TestServiceEmbeddingProgressSkipsSourceDeletedDuringBatch(t *testing.T) {
	store := serviceStore(t)
	id := serviceSource(t, store, "reader.app", "Reservation source removed while its batch is running.")
	e := &serviceEmbedder{before: func(ctx context.Context, _ int32) error {
		return store.DeleteObservationRecord(ctx, id)
	}}
	s := newService(t, store, e, semantic.Options{})
	if err := s.Backfill(context.Background()); err != nil || !s.Status().Complete || s.Status().IndexedSources != 0 {
		t.Fatalf("deleted source blocked backfill: %+v %v", s.Status(), err)
	}
	model, _ := e.Identity(context.Background())
	if _, found, err := store.GetSemanticProgress(context.Background(), id, model.Key()); err != nil || found {
		t.Fatalf("deleted source retained progress: found=%v err=%v", found, err)
	}
}
