package semantic_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

type serviceEmbedder struct {
	digest    string
	documents atomic.Int32
	before    func(context.Context, int32) error
	query     func(context.Context) error
}

func (e *serviceEmbedder) Identity(context.Context) (semantic.Model, error) {
	digest := e.digest
	if digest == "" {
		digest = "a"
	}
	return semantic.Model{Name: "fixture:latest", Digest: strings.Repeat(digest, 64), Dimensions: 2, Format: semantic.FormatPlain}, nil
}

type sourcePageProbe struct {
	*durable.Store
	pages atomic.Int32
}

func TestServiceRevisitIsSearchableWithoutReembeddingOrClosingActivity(t *testing.T) {
	ctx := context.Background()
	store := serviceStore(t)
	o, _ := capture.SanitizeObservation("reader.app", "work.example", "Mira owns the rollback.", 512)
	first := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	visit := first.AddDate(0, 0, 2)
	if err := store.PutObservationAt(ctx, o, first); err != nil {
		t.Fatal(err)
	}
	e := &serviceEmbedder{}
	s, err := semantic.NewService(store, store, e, semantic.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Start()
	waitServiceReady(t, s, 1)
	if err := store.RecordObservationSighting(ctx, o, visit); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordObservationSighting(ctx, o, visit.Add(40*time.Minute)); err != nil {
		t.Fatal(err)
	}
	query := semantic.Query{Limit: 5, Start: visit.Add(-time.Minute), End: visit.Add(time.Minute)}
	hits, err := s.Search(ctx, "who handles reverting the deployment", query)
	if err != nil || len(hits) != 1 || hits[0].SourceID != o.ContentID() || !hits[0].CapturedAt.Equal(visit) {
		t.Fatalf("semantic visit was lost before ranking: %+v %v", hits, err)
	}
	if e.documents.Load() != 1 {
		t.Fatalf("unchanged source was embedded again: %d", e.documents.Load())
	}
	query.Start, query.End = visit.Add(time.Minute), visit.Add(2*time.Minute)
	hits, err = s.Search(ctx, "reverting the deployment", query)
	if err != nil || len(hits) != 0 {
		t.Fatalf("unseen semantic time window leaked source: %+v %v", hits, err)
	}
}

func (p *sourcePageProbe) ListObservationPage(ctx context.Context, request storage.ObservationPageRequest) (storage.ObservationPage, error) {
	p.pages.Add(1)
	return p.Store.ListObservationPage(ctx, request)
}

func TestServiceNewCaptureUsesIncrementalQueueWithoutRescanningSavedSources(t *testing.T) {
	store := serviceStore(t)
	serviceSource(t, store, "reader.app", "A reservation for two on Friday")
	probe := &sourcePageProbe{Store: store}
	e := &serviceEmbedder{}
	s, err := semantic.NewService(probe, store, e, semantic.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Start()
	waitServiceReady(t, s, 1)
	pages := probe.pages.Load()
	id := serviceSource(t, store, "editor.app", "A new compiler diagnostic")
	s.NotifySource(id)
	waitServiceReady(t, s, 2)
	if probe.pages.Load() != pages || e.documents.Load() != 2 {
		t.Fatalf("incremental update rescanned sources: pages=%d -> %d embeddings=%d", pages, probe.pages.Load(), e.documents.Load())
	}
}

func TestServiceModelManifestChangeRebuildsCompatibleVectors(t *testing.T) {
	store := serviceStore(t)
	serviceSource(t, store, "reader.app", "A reservation for two on Friday")
	e := &serviceEmbedder{}
	s := newService(t, store, e, semantic.Options{})
	if err := s.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.digest = "b"
	if err := s.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.documents.Load() != 2 || !s.Status().Complete || s.Status().IndexedSources != 1 {
		t.Fatalf("model rebuild status=%+v calls=%d", s.Status(), e.documents.Load())
	}
}

func TestServiceOverflowRepairsSourcesBeforeAnIncompleteCursor(t *testing.T) {
	store := serviceStore(t)
	serviceSource(t, store, "reader.app", "A reservation for two on Friday")
	serviceSource(t, store, "editor.app", "The compiler is running")
	page, err := store.ListObservationPage(context.Background(), storage.ObservationPageRequest{Limit: 4, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 2 {
		t.Fatalf("sources=%+v err=%v", page, err)
	}
	e := &serviceEmbedder{}
	m, _ := e.Identity(context.Background())
	last := page.Observations[1]
	document := semantic.Document{SourceID: last.ContentID(), AppID: last.AppID(), CapturedAt: page.CapturedAt[1], TextBytes: len(last.Text()), Model: m, Chunks: []semantic.Chunk{{Start: 0, End: len(last.Text()), Vector: []float32{1, 0}}}}
	if err := store.PutSemanticDocument(context.Background(), document); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSemanticCheckpoint(context.Background(), semantic.Checkpoint{ModelKey: m.Key(), After: last.ContentID(), Indexed: 1}); err != nil {
		t.Fatal(err)
	}
	s := newService(t, store, e, semantic.Options{})
	for i := 0; i < 1024; i++ {
		s.NotifySource(fmt.Sprintf("%064x", i+1))
	}
	s.NotifySource(page.Observations[0].ContentID()) // overflows; sorts before the old cursor
	var lastError error
	for attempt := 0; attempt < 20; attempt++ {
		lastError = s.Backfill(context.Background())
		if lastError == nil && s.Status().Complete {
			if s.Status().IndexedSources != 2 || e.documents.Load() != 1 {
				t.Fatalf("repair coverage=%+v calls=%d", s.Status(), e.documents.Load())
			}
			return
		}
	}
	t.Fatalf("overflow did not finish repair: %+v err=%v", s.Status(), lastError)
}

func waitServiceReady(t *testing.T, s *semantic.Service, sources int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		status := s.Status()
		if status.Complete && status.IndexedSources == sources {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("index did not become ready: %+v", status)
		case <-poll.C:
		}
	}
}

func (e *serviceEmbedder) EmbedDocuments(ctx context.Context, _ semantic.Model, input []string) ([][]float32, error) {
	n := e.documents.Add(1)
	if e.before != nil {
		if err := e.before(ctx, n); err != nil {
			return nil, err
		}
	}
	result := make([][]float32, len(input))
	for i, text := range input {
		result[i] = []float32{0, 1}
		if strings.Contains(text, "reservation") {
			result[i] = []float32{1, 0}
		}
	}
	return result, nil
}

func (e *serviceEmbedder) EmbedQuery(ctx context.Context, _ semantic.Model, _ string) ([]float32, error) {
	if e.query != nil {
		if err := e.query(ctx); err != nil {
			return nil, err
		}
	}
	return []float32{1, 0}, nil
}

func serviceStore(t *testing.T) *durable.Store {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "memory")
	if err := bolt.CreatePrivateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	store, err := durable.OpenDurable(filepath.Join(directory, "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func serviceSource(t *testing.T, store *durable.Store, app, text string) string {
	t.Helper()
	observation, err := capture.SanitizeObservation(app, "", text, capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(context.Background(), observation, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return observation.ContentID()
}

func newService(t *testing.T, store *durable.Store, e *serviceEmbedder, options semantic.Options) *semantic.Service {
	t.Helper()
	s, err := semantic.NewService(store, store, e, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestServiceRestartsFromEncryptedVectorsAndNeverEmbedsOwnUI(t *testing.T) {
	store := serviceStore(t)
	want := serviceSource(t, store, "reader.app", "A reservation for two on Friday evening")
	serviceSource(t, store, "editor.app", "A Go compiler rejects unused variables")
	serviceSource(t, store, appidentity.BundleID, "A private query displayed inside Blur")
	e := &serviceEmbedder{}
	s := newService(t, store, e, semantic.Options{})
	if err := s.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := s.Status(); !status.Complete || status.IndexedSources != 2 || status.SkippedSources != 1 || status.Chunks != 2 {
		t.Fatalf("coverage=%+v", status)
	}
	if e.documents.Load() != 2 {
		t.Fatalf("embedded %d sources", e.documents.Load())
	}
	s.Close()
	e = &serviceEmbedder{}
	s = newService(t, store, e, semantic.Options{})
	if err := s.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.documents.Load() != 0 {
		t.Fatal("restart re-embedded saved sources")
	}
	hits, err := s.Search(context.Background(), "Where am I going for dinner?", semantic.Query{Limit: 1})
	if err != nil || len(hits) != 1 || hits[0].SourceID != want {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
}

func TestServiceResumesAfterCommittedSourceOnEmbeddingFailure(t *testing.T) {
	store := serviceStore(t)
	serviceSource(t, store, "reader.app", "A reservation for two on Friday")
	serviceSource(t, store, "editor.app", "The compiler is running")
	failure := errors.New("fixture interrupted")
	e := &serviceEmbedder{before: func(_ context.Context, n int32) error {
		if n == 2 {
			return failure
		}
		return nil
	}}
	s := newService(t, store, e, semantic.Options{})
	if err := s.Backfill(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("interrupted=%v", err)
	}
	m, _ := e.Identity(context.Background())
	checkpoint, found, err := store.GetSemanticCheckpoint(context.Background(), m.Key())
	if err != nil || !found || checkpoint.After == "" || checkpoint.Complete || checkpoint.Indexed != 1 {
		t.Fatalf("checkpoint=%+v found=%v err=%v", checkpoint, found, err)
	}
	s.Close()
	e = &serviceEmbedder{}
	s = newService(t, store, e, semantic.Options{})
	if err := s.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.documents.Load() != 1 || s.Status().IndexedSources != 2 {
		t.Fatalf("resume calls=%d status=%+v", e.documents.Load(), s.Status())
	}
}

func TestServiceInvalidationDoesNotResurrectSourceAfterStubbornEmbedding(t *testing.T) {
	store := serviceStore(t)
	id := serviceSource(t, store, "reader.app", "A reservation for two on Friday")
	entered, release := make(chan struct{}), make(chan struct{})
	e := &serviceEmbedder{before: func(context.Context, int32) error { close(entered); <-release; return nil }}
	s := newService(t, store, e, semantic.Options{})
	result := make(chan error, 1)
	go func() { result <- s.Backfill(context.Background()) }()
	<-entered
	s.Invalidate()
	if err := store.DeleteObservationRecord(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("late result=%v", err)
	}
	if _, found, err := store.GetSemanticDocument(context.Background(), id); err != nil || found {
		t.Fatalf("deleted vectors found=%v err=%v", found, err)
	}
	if hits, err := s.Search(context.Background(), "dinner", semantic.Query{Limit: 1}); !errors.Is(err, semantic.ErrUnavailable) || len(hits) != 0 {
		t.Fatalf("deleted cache hits=%v err=%v", hits, err)
	}
}

type saverPolicy struct{}

func (saverPolicy) Effective(context.Context) (power.Effective, error) {
	return power.Resolve(power.Config{Mode: power.Saver}, power.SystemState{})
}

func TestServiceSaverPausesNewEmbeddingsButAllowsExplicitRecall(t *testing.T) {
	store := serviceStore(t)
	serviceSource(t, store, "reader.app", "A reservation for two on Friday")
	e := &serviceEmbedder{}
	s := newService(t, store, e, semantic.Options{})
	if err := s.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	serviceSource(t, store, "editor.app", "A new source to index")
	e = &serviceEmbedder{}
	s = newService(t, store, e, semantic.Options{Policy: saverPolicy{}})
	if err := s.Backfill(context.Background()); !errors.Is(err, semantic.ErrPolicy) {
		t.Fatalf("power error=%v", err)
	}
	if e.documents.Load() != 0 || s.Status().State != "paused" {
		t.Fatalf("background ran: %+v", s.Status())
	}
	hits, err := s.Search(context.Background(), "dinner", semantic.Query{Limit: 1})
	if err != nil || len(hits) != 1 {
		t.Fatalf("explicit recall=%+v err=%v", hits, err)
	}
}

func TestServiceConcurrentCloseCancelsAndDrainsQuery(t *testing.T) {
	store := serviceStore(t)
	serviceSource(t, store, "reader.app", "A reservation for two on Friday")
	entered := make(chan struct{})
	e := &serviceEmbedder{query: func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }}
	s := newService(t, store, e, semantic.Options{})
	if err := s.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	queryDone := make(chan error, 1)
	go func() { _, err := s.Search(context.Background(), "dinner", semantic.Query{Limit: 1}); queryDone <- err }()
	<-entered
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.Close() }()
	go func() { defer wg.Done(); s.Close() }()
	wg.Wait()
	if err := <-queryDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("query not canceled: %v", err)
	}
}
