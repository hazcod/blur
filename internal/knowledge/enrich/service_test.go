package enrich_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

type generator struct {
	mu               sync.Mutex
	calls            int
	identity         string
	entered, release chan struct{}
}

func (g *generator) Identity(context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.identity == "" {
		return "local-fixture", nil
	}
	return g.identity, nil
}
func (g *generator) CountInputTokens(context.Context, []derive.Evidence) (int, error) {
	return 512, nil
}
func (g *generator) Generate(_ context.Context, evidence []derive.Evidence) ([]derive.Proposal, error) {
	g.mu.Lock()
	g.calls++
	entered, release := g.entered, g.release
	g.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	if !strings.Contains(evidence[0].Text, "TAIL-ATLAS") {
		return nil, nil
	}
	return []derive.Proposal{{Kind: knowledge.Project, Name: "Atlas", Fact: "Atlas is a private local project.", SourceID: evidence[0].SourceID}}, nil
}

type policy struct{ mode power.Mode }

func (p policy) Effective(context.Context) (power.Effective, error) {
	return power.Resolve(power.Config{Mode: p.mode}, power.SystemState{})
}

func source(t *testing.T, s *durable.Store, text string) capture.SanitizedObservation {
	t.Helper()
	o, err := capture.SanitizeObservation("editor.app", "work.example", text, capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(context.Background(), o, time.Now()); err != nil {
		t.Fatal(err)
	}
	return o
}
func complete(t *testing.T, s *enrich.Service) {
	t.Helper()
	for i := 0; i < 100; i++ {
		err := s.Backfill(context.Background())
		if err == nil {
			return
		}
		if !errors.Is(err, enrich.ErrMoreWork) {
			t.Fatal(err)
		}
	}
	t.Fatal("backfill did not terminate")
}

func TestBackfillResumesInsideLongSourceAndIncrementalNoteAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	key := securestore.OfflineFixtureKey()
	s, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	o := source(t, s, strings.Repeat("Unrelated context. ", 500)+"TAIL-ATLAS")
	g := &generator{}
	worker, err := enrich.NewService(s, s, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Backfill(context.Background()); !errors.Is(err, enrich.ErrMoreWork) {
		t.Fatalf("long source marked complete after prefix: %v", err)
	}
	progress, found, err := s.GetEnrichmentProgress(context.Background(), o.ContentID())
	if err != nil || !found || progress.Complete || progress.Next == 0 {
		t.Fatalf("missing resumable source cursor: %+v %v", progress, err)
	}
	worker.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	worker, err = enrich.NewService(s, s, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	complete(t, worker)
	entities, err := s.FindInferredKnowledge(context.Background(), "Atlas")
	if err != nil || len(entities) != 1 {
		t.Fatalf("tail evidence omitted: %+v %v", entities, err)
	}
	g.mu.Lock()
	calls := g.calls
	g.mu.Unlock()
	complete(t, worker)
	g.mu.Lock()
	if g.calls != calls {
		t.Fatal("restart/repeated pass extracted completed content again")
	}
	g.mu.Unlock()
	note := source(t, s, "TAIL-ATLAS new explicit note")
	worker.NotifySource(note.ContentID())
	complete(t, worker)
	entities, _ = s.FindInferredKnowledge(context.Background(), "Atlas")
	if len(entities) != 1 || len(entities[0].Facts) != 2 {
		t.Fatalf("incremental evidence missing: %+v", entities)
	}
}

func TestDeletionWhileProviderIgnoresCancellationCannotPublishMemory(t *testing.T) {
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o := source(t, s, "TAIL-ATLAS source being deleted")
	g := &generator{entered: make(chan struct{}, 1), release: make(chan struct{})}
	worker, _ := enrich.NewService(s, s, g, enrich.Options{})
	defer worker.Close()
	done := make(chan error, 1)
	go func() { done <- worker.Backfill(context.Background()) }()
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not enter")
	}
	if err := s.DeleteObservationRecord(context.Background(), o.ContentID()); err != nil {
		t.Fatal(err)
	}
	close(g.release)
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, enrich.ErrMoreWork) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not finish")
	}
	entities, err := s.FindInferredKnowledge(context.Background(), "Atlas")
	if err != nil || len(entities) != 0 {
		t.Fatalf("late extraction resurrected source: %+v %v", entities, err)
	}
}

func TestPowerSaverDoesNotSendSourceToModel(t *testing.T) {
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	source(t, s, "TAIL-ATLAS")
	g := &generator{}
	worker, _ := enrich.NewService(s, s, g, enrich.Options{Policy: policy{power.Saver}})
	defer worker.Close()
	if err := worker.Backfill(context.Background()); !errors.Is(err, enrich.ErrPolicy) {
		t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.calls != 0 || worker.Status().State != "paused" {
		t.Fatal("background model ran during saver")
	}
}

func TestModelReplacementCannotPublishOldGenerationClaims(t *testing.T) {
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	source(t, s, "TAIL-ATLAS")
	g := &generator{entered: make(chan struct{}, 1), release: make(chan struct{})}
	worker, _ := enrich.NewService(s, s, g, enrich.Options{})
	defer worker.Close()
	done := make(chan error, 1)
	go func() { done <- worker.Backfill(context.Background()) }()
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not enter")
	}
	g.mu.Lock()
	g.identity = "replacement-local-model"
	g.mu.Unlock()
	close(g.release)
	select {
	case err := <-done:
		if !errors.Is(err, model.ErrOllamaModelChanged) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not finish")
	}
	entries, err := s.FindInferredKnowledge(context.Background(), "Atlas")
	if err != nil || len(entries) != 0 {
		t.Fatal("stale generation published", entries, err)
	}
	complete(t, worker)
	entries, err = s.FindInferredKnowledge(context.Background(), "Atlas")
	if err != nil || len(entries) != 1 {
		t.Fatal("replacement generation did not recover", entries, err)
	}
}

type orderedSources struct {
	*durable.Store
	rows []capture.SanitizedObservation
}

func (s orderedSources) ListObservationPage(_ context.Context, r storage.ObservationPageRequest) (storage.ObservationPage, error) {
	start := 0
	if r.After != "" {
		start = len(s.rows)
		for i, row := range s.rows {
			if row.ContentID() == r.After {
				start = i + 1
				break
			}
		}
	}
	end := min(start+r.Limit, len(s.rows))
	rows := s.rows[start:end]
	return storage.ObservationPage{Observations: rows, CapturedAt: make([]time.Time, len(rows)), HasMore: end < len(s.rows)}, nil
}

func TestQueueOverflowRepairsSourcesBeforeAnIncompleteCursor(t *testing.T) {
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	older := source(t, s, "TAIL-ATLAS older source before the cursor")
	covered := source(t, s, "Already covered source")
	key := enrich.ModelKey("local-fixture")
	if err := s.ApplyEnrichment(context.Background(), enrich.Chunk{SourceID: covered.ContentID(), ModelKey: key, End: len(covered.Text()), Next: len(covered.Text()), Complete: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutEnrichmentCheckpoint(context.Background(), enrich.Checkpoint{ModelKey: key, After: covered.ContentID()}); err != nil {
		t.Fatal(err)
	}
	worker, _ := enrich.NewService(orderedSources{Store: s, rows: []capture.SanitizedObservation{older, covered}}, s, &generator{}, enrich.Options{})
	defer worker.Close()
	for i := 1; i <= 1024; i++ {
		worker.NotifySource(fmt.Sprintf("%064x", i))
	}
	worker.NotifySource(older.ContentID()) // overflowed notification sorts before durable cursor
	if err := worker.Backfill(context.Background()); !errors.Is(err, enrich.ErrMoreWork) {
		t.Fatalf("reported full coverage despite overflow before cursor: %v", err)
	}
	complete(t, worker)
	progress, found, err := s.GetEnrichmentProgress(context.Background(), older.ContentID())
	if err != nil || !found || !progress.Complete {
		t.Fatalf("older capture was skipped: %+v %v", progress, err)
	}
}
