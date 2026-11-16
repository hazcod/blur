package semantic_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

type captureWakeSemanticSources struct {
	*durable.Store
	legacyPages, capturePages, historicalParts atomic.Int32
	historicalEdition                          string
	failOnce                                   atomic.Bool
	afterMu                                    sync.Mutex
	afterPage                                  func()
}

func (s *captureWakeSemanticSources) ListObservationPage(ctx context.Context, r storage.ObservationPageRequest) (storage.ObservationPage, error) {
	s.legacyPages.Add(1)
	return s.Store.ListObservationPage(ctx, r)
}
func (s *captureWakeSemanticSources) ListCaptureEvidencePage(ctx context.Context, r storage.CaptureEvidencePageRequest) (storage.CaptureEvidencePage, error) {
	if s.failOnce.CompareAndSwap(true, false) {
		s.capturePages.Add(1)
		return storage.CaptureEvidencePage{}, context.DeadlineExceeded
	}
	p, err := s.Store.ListCaptureEvidencePage(ctx, r)
	for _, source := range p.Sources {
		if provenance, ok := source.Provenance(); ok && provenance.EditionID == s.historicalEdition {
			s.historicalParts.Add(1)
		}
	}
	s.capturePages.Add(1)
	s.afterMu.Lock()
	after := s.afterPage
	s.afterPage = nil
	s.afterMu.Unlock()
	if after != nil {
		after()
	}
	return p, err
}
func (s *captureWakeSemanticSources) assertNoHistory(t *testing.T) {
	t.Helper()
	if s.legacyPages.Load() != 0 || s.historicalParts.Load() != 0 {
		t.Fatal("capture wake rescanned completed semantic source history", s.legacyPages.Load(), s.historicalParts.Load())
	}
}
func wakeSemanticCaptures(s *semantic.Service) {
	if w, ok := any(s).(interface{ WakeCaptures() }); ok {
		w.WakeCaptures()
	} else {
		s.Wake()
	}
}
func waitSemanticCaptureCheck(t *testing.T, s *semantic.Service, sources *captureWakeSemanticSources, after int32, ready bool, changes <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		if sources.capturePages.Load() > after && s.Status().Complete == ready {
			return
		}
		select {
		case <-timer.C:
			t.Fatal("semantic capture tail did not settle", s.Status(), sources.capturePages.Load(), after)
		case <-changes:
		}
	}
}

func TestSemanticCaptureWakePreservesDirectoriesAndStartupRepairsOfflineSources(t *testing.T) {
	ctx := context.Background()
	path, key := filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey()
	store, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	for i := 0; i < 32; i++ {
		serviceSource(t, store, "reader.app", fmt.Sprintf("Completed old semantic source %d", i))
	}
	old := semanticCapture(t, store, "Completed old multipart semantic source.", true)
	sources := &captureWakeSemanticSources{Store: store, historicalEdition: old.ContentID()}
	e := &serviceEmbedder{}
	changes := make(chan struct{}, 1)
	options := semantic.Options{OnChange: func() {
		select {
		case changes <- struct{}{}:
		default:
		}
	}}
	s, err := semantic.NewService(sources, store, e, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	s.Start()
	waitSemanticCaptureCheck(t, s, sources, 0, true, changes)
	identity, _ := e.Identity(ctx)
	checkpoint, _, err := store.GetSemanticCheckpoint(ctx, identity.Key())
	if err != nil || !checkpoint.Complete || !checkpoint.CaptureComplete {
		t.Fatal("fixture directory incomplete", err)
	}
	base := e.documents.Load()
	sources.legacyPages.Store(0)
	sources.historicalParts.Store(0)
	var before int32
	for range 6 {
		before = sources.capturePages.Load()
		wakeSemanticCaptures(s)
		waitSemanticCaptureCheck(t, s, sources, before, true, changes)
		sources.assertNoHistory(t)
	}
	before = sources.capturePages.Load()
	sources.afterMu.Lock()
	sources.afterPage = func() { wakeSemanticCaptures(s) }
	sources.afterMu.Unlock()
	wakeSemanticCaptures(s)
	waitSemanticCaptureCheck(t, s, sources, before+1, true, changes)
	sources.assertNoHistory(t)
	if e.documents.Load() != base {
		t.Fatal("routine capture wake repeated embeddings")
	}
	current, _, err := store.GetSemanticCheckpoint(ctx, identity.Key())
	if err != nil || current.After != checkpoint.After || current.CaptureAfter != checkpoint.CaptureAfter {
		t.Fatal("capture wake discarded semantic cursor", err)
	}
	semanticCapture(t, store, "Appended semantic source waits for its index.", false)
	before = sources.capturePages.Load()
	wakeSemanticCaptures(s)
	waitSemanticCaptureCheck(t, s, sources, before, false, changes)
	sources.assertNoHistory(t)
	drainSemanticCapture(t, store)
	before = sources.capturePages.Load()
	sources.failOnce.Store(true)
	wakeSemanticCaptures(s)
	// A failed tail check retries on its existing deadline path without a
	// second notification or resetting either completed source directory.
	waitSemanticCaptureCheck(t, s, sources, before+1, true, changes)
	sources.assertNoHistory(t)
	if e.documents.Load() != base+1 {
		t.Fatal("indexed appended semantic source not embedded once", e.documents.Load(), base)
	}
	s.Close()
	semanticCapture(t, store, "Appended semantic source while service was closed.", true)
	serviceSource(t, store, "reader.app", "Unnotified legacy source while service was closed.")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	sources = &captureWakeSemanticSources{Store: store, historicalEdition: old.ContentID()}
	s, err = semantic.NewService(sources, store, e, options)
	if err != nil {
		t.Fatal(err)
	}
	s.Start()
	waitSemanticCaptureCheck(t, s, sources, 0, true, changes)
	if sources.legacyPages.Load() != 9 || sources.historicalParts.Load() != 1 {
		t.Fatal("startup must repair source directories once", sources.legacyPages.Load(), sources.historicalParts.Load())
	}
	if e.documents.Load() != base+3 {
		t.Fatal("restart missed offline legacy or appended indexed semantic source", e.documents.Load(), base)
	}
	sources.legacyPages.Store(0)
	sources.historicalParts.Store(0)
	before = sources.capturePages.Load()
	s.WakeCaptures()
	waitSemanticCaptureCheck(t, s, sources, before, true, changes)
	sources.assertNoHistory(t)
}
