package enrich_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

type captureWakeLibrarySources struct {
	*durable.Store
	mu                           sync.Mutex
	legacyPages, historicalParts int
	capturePages                 int
	historicalEdition            string
	pageError                    error
	afterPage                    func()
}

func (s *captureWakeLibrarySources) ListObservationPage(ctx context.Context, r storage.ObservationPageRequest) (storage.ObservationPage, error) {
	s.mu.Lock()
	s.legacyPages++
	s.mu.Unlock()
	return s.Store.ListObservationPage(ctx, r)
}
func (s *captureWakeLibrarySources) ListCaptureEvidencePage(ctx context.Context, r storage.CaptureEvidencePageRequest) (storage.CaptureEvidencePage, error) {
	s.mu.Lock()
	s.capturePages++
	err, after := s.pageError, s.afterPage
	s.pageError, s.afterPage = nil, nil
	s.mu.Unlock()
	if err != nil {
		return storage.CaptureEvidencePage{}, err
	}
	p, err := s.Store.ListCaptureEvidencePage(ctx, r)
	if after != nil {
		after()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, source := range p.Sources {
		if provenance, ok := source.Provenance(); ok && provenance.EditionID == s.historicalEdition {
			s.historicalParts++
		}
	}
	return p, err
}

func waitLibraryCaptureCheck(t *testing.T, worker *enrich.Service, sources *captureWakeLibrarySources, after int, ready bool, changes <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		sources.mu.Lock()
		pages := sources.capturePages
		sources.mu.Unlock()
		if pages > after && worker.Status().Complete == ready {
			return
		}
		select {
		case <-timer.C:
			t.Fatal("Library capture tail did not settle", pages, after, worker.Status())
		case <-changes:
		}
	}
}

func TestLibraryCaptureRetryTimerDoesNotRepairCompletedHistory(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	source(t, store, "Old Atlas source already completely processed.")
	old := publishedLibraryCapture(t, store, "Old multipart Atlas source already completely processed.")
	sources := &captureWakeLibrarySources{Store: store, historicalEdition: old.ContentID()}
	g := &refusingLibraryGenerator{digest: "capture-retry-timer", calls: map[string]int{}}
	changes := make(chan struct{}, 1)
	worker, err := enrich.NewService(sources, store, g, enrich.Options{OnChange: func() {
		select {
		case changes <- struct{}{}:
		default:
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	complete(t, worker)
	sources.reset()
	sources.mu.Lock()
	before := sources.capturePages
	sources.mu.Unlock()
	worker.Start()
	worker.WakeCaptures()
	waitLibraryCaptureCheck(t, worker, sources, before, true, changes)
	publishedLibraryCapture(t, store, "New Atlas source is published but index pending.", false)
	sources.mu.Lock()
	before = sources.capturePages
	sources.pageError = context.DeadlineExceeded
	sources.mu.Unlock()
	worker.WakeCaptures()
	// No second wake: a deadline retry must resume the tail and report the
	// pending source, without converting the retry timer into a repair pass.
	waitLibraryCaptureCheck(t, worker, sources, before+1, false, changes)
	sources.assertNoHistory(t)
	for range 64 {
		more, err := store.IndexPendingCaptures(ctx, 16)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	sources.mu.Lock()
	before = sources.capturePages
	sources.mu.Unlock()
	waitLibraryCaptureCheck(t, worker, sources, before, true, changes)
	sources.assertNoHistory(t)
	worker.Close()
	if len(g.calls) != 3 {
		t.Fatal("tail retry lost new source or repeated inference", len(g.calls))
	}
}

func TestLibraryCaptureWakeRetainsFailedAndConcurrentTailChecks(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	source(t, store, "Legacy Atlas evidence already processed.")
	old := publishedLibraryCapture(t, store, "Old multipart Atlas evidence already processed.")
	sources := &captureWakeLibrarySources{Store: store, historicalEdition: old.ContentID()}
	g := &refusingLibraryGenerator{digest: "capture-wake-failure-model", calls: map[string]int{}}
	worker, err := enrich.NewService(sources, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	complete(t, worker)
	sources.reset()
	publishedLibraryCapture(t, store, "New multipart Atlas evidence after temporary directory failure.")
	failure := errors.New("synthetic metadata failure")
	sources.mu.Lock()
	sources.pageError = failure
	sources.mu.Unlock()
	worker.WakeCaptures()
	if err := worker.Backfill(ctx); !errors.Is(err, failure) {
		t.Fatal("fixture missed capture-tail failure", err)
	}
	// The first request must survive without another notification.
	complete(t, worker)
	sources.assertNoHistory(t)
	if len(g.calls) != 3 {
		t.Fatal("failed capture check lost appended source", len(g.calls))
	}
	sources.mu.Lock()
	sources.afterPage = worker.WakeCaptures
	sources.mu.Unlock()
	worker.WakeCaptures()
	if err := worker.Backfill(ctx); !errors.Is(err, enrich.ErrMoreWork) {
		t.Fatal("wake during final directory read was lost", err)
	}
	complete(t, worker)
	sources.assertNoHistory(t)
	if len(g.calls) != 3 {
		t.Fatal("concurrent tail check repeated model work")
	}
	worker.Close()
	worker.WakeCaptures()
	if err := worker.Backfill(ctx); !errors.Is(err, enrich.ErrUnavailable) {
		t.Fatal("closed capture worker restarted", err)
	}
}
func (s *captureWakeLibrarySources) reset() {
	s.mu.Lock()
	s.legacyPages, s.historicalParts = 0, 0
	s.mu.Unlock()
}
func (s *captureWakeLibrarySources) assertNoHistory(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.legacyPages != 0 || s.historicalParts != 0 {
		t.Fatal("capture notification rescanned completed source history", s.legacyPages, s.historicalParts)
	}
}

func wakeLibraryCaptures(s *enrich.Service) {
	if w, ok := any(s).(interface{ WakeCaptures() }); ok {
		w.WakeCaptures()
	} else {
		s.Wake()
	}
}

func TestLibraryCaptureWakePreservesCompletedDirectoriesAndDiscoversOnlyAppendedParts(t *testing.T) {
	ctx := context.Background()
	path, key := filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey()
	store, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	for i := 0; i < 32; i++ {
		source(t, store, fmt.Sprintf("Completed historical Atlas source %d", i))
	}
	old := publishedLibraryCapture(t, store, "Completed historical multipart Atlas source.")
	sources := &captureWakeLibrarySources{Store: store, historicalEdition: old.ContentID()}
	g := &refusingLibraryGenerator{digest: "capture-wake-model", calls: map[string]int{}}
	worker, err := enrich.NewService(sources, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { worker.Close() }()
	complete(t, worker)
	checkpoint, _, err := store.GetEnrichmentCheckpoint(ctx)
	if err != nil || !checkpoint.Complete || !checkpoint.CaptureComplete {
		t.Fatal("fixture directories not complete", err)
	}
	initialCalls := len(g.calls)
	sources.reset()
	for range 6 {
		wakeLibraryCaptures(worker)
		complete(t, worker)
	}
	sources.assertNoHistory(t)
	if len(g.calls) != initialCalls {
		t.Fatal("duplicate capture wakes repeated model work")
	}
	current, _, err := store.GetEnrichmentCheckpoint(ctx)
	if err != nil || current.After != checkpoint.After || current.CaptureAfter != checkpoint.CaptureAfter {
		t.Fatal("capture wake reset completed directory cursor", err)
	}
	publishedLibraryCapture(t, store, "Appended Atlas source awaits bounded indexing.", false)
	wakeLibraryCaptures(worker)
	if err := worker.Backfill(ctx); err == nil || worker.Status().Complete {
		t.Fatal("pending index falsely complete", err)
	}
	sources.assertNoHistory(t)
	for range 64 {
		more, err := store.IndexPendingCaptures(ctx, 16)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	wakeLibraryCaptures(worker)
	complete(t, worker)
	sources.assertNoHistory(t)
	if len(g.calls) != initialCalls+1 {
		t.Fatal("new indexed part not processed exactly once", len(g.calls), initialCalls)
	}
	// An appended edition already indexed while Blur was closed must be found
	// after restart by one tail check, without resetting legacy coverage.
	worker.Close()
	publishedLibraryCapture(t, store, "Appended while Library was stopped.")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	sources = &captureWakeLibrarySources{Store: store, historicalEdition: old.ContentID()}
	worker, err = enrich.NewService(sources, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	complete(t, worker)
	sources.assertNoHistory(t)
	if len(g.calls) != initialCalls+2 {
		t.Fatal("restart missed appended indexed source", len(g.calls), initialCalls)
	}
}
