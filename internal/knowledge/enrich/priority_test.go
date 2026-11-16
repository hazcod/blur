package enrich_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type priorityLibraryGenerator struct {
	calls   int
	failure error
}

func (*priorityLibraryGenerator) Identity(context.Context) (string, error) {
	return "priority-fixture", nil
}
func (*priorityLibraryGenerator) CountInputTokens(context.Context, []derive.Evidence) (int, error) {
	return 10, nil
}
func (g *priorityLibraryGenerator) Generate(context.Context, []derive.Evidence) ([]derive.Proposal, error) {
	g.calls++
	if g.calls == 1 {
		return nil, g.failure
	}
	return nil, nil
}

type priorityLibraryPolicy struct{ mode power.Mode }

func (p *priorityLibraryPolicy) Effective(context.Context) (power.Effective, error) {
	return power.Resolve(power.Config{Mode: p.mode}, power.SystemState{})
}
func TestPreemptedLibraryKeepsCursorAndRechecksSourcePolicyAndClose(t *testing.T) {
	for _, operation := range []string{"retry", "deletion", "policy", "close", "actual-error"} {
		t.Run(operation, func(t *testing.T) {
			store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			observation := source(t, store, "Synthetic library source without inferred facts")
			generator := &priorityLibraryGenerator{failure: model.ErrBackgroundPreempted}
			if operation == "actual-error" {
				generator.failure = errors.New("synthetic actual provider error")
			}
			policy := &priorityLibraryPolicy{mode: power.Balanced}
			worker, err := enrich.NewService(store, store, generator, enrich.Options{Policy: policy})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Close()
			worker.NotifySource(observation.ContentID())
			err = worker.Backfill(context.Background())
			if operation == "actual-error" {
				if errors.Is(err, enrich.ErrMoreWork) || worker.Status().State != "unavailable" {
					t.Fatal("actual model error hidden as priority deferral")
				}
				return
			}
			if !errors.Is(err, enrich.ErrMoreWork) || worker.Status().State != "indexing" || worker.Status().ErrorCode != "" || worker.Status().DeferredReason != "foreground" {
				t.Fatal("preemption presented as provider failure", err, worker.Status())
			}
			if _, found, err := store.GetEnrichmentProgress(context.Background(), observation.ContentID()); err != nil || found {
				t.Fatal("preempted source advanced durable progress", err)
			}
			checkpoint, _, err := store.GetEnrichmentCheckpoint(context.Background())
			if err != nil || checkpoint.After != "" || checkpoint.Complete {
				t.Fatal("preempted library advanced directory cursor", err)
			}
			switch operation {
			case "deletion":
				if err := store.DeleteObservationRecord(context.Background(), observation.ContentID()); err != nil {
					t.Fatal(err)
				}
			case "policy":
				policy.mode = power.Saver
			case "close":
				worker.Close()
			}
			err = worker.Backfill(context.Background())
			if operation == "retry" {
				if err != nil && !errors.Is(err, enrich.ErrMoreWork) || generator.calls != 2 {
					t.Fatal("deferred library source was lost", err, generator.calls)
				}
			} else if generator.calls != 1 {
				t.Fatal("library retried after source/policy/close invalidation")
			}
		})
	}
}

func TestLibraryStatusActiveReflectsActualBackfillAndCompletion(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	source(t, store, "Synthetic source for activity status")
	generator := &generator{entered: make(chan struct{}, 1), release: make(chan struct{})}
	worker, err := enrich.NewService(store, store, generator, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	done := make(chan error, 1)
	go func() { done <- worker.Backfill(context.Background()) }()
	select {
	case <-generator.entered:
	case <-time.After(time.Second):
		t.Fatal("backfill did not enter real generator call")
	}
	for range 100 {
		status := worker.Status()
		if !status.Active || status.ProcessedChunks != 0 {
			t.Fatal("status did not reflect actual active backfill", status)
		}
	}
	generator.mu.Lock()
	calls := generator.calls
	generator.mu.Unlock()
	if calls != 1 {
		t.Fatal("status read performed model work")
	}
	close(generator.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	status := worker.Status()
	if status.Active || status.ProcessedChunks != 1 || status.LastCompletedAt.IsZero() || !status.Complete {
		t.Fatal("backfill completion status inaccurate", status)
	}
}

func TestLibraryStatusRetainsTimeoutWhileRetryingBackfill(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	source(t, store, "Synthetic source for a timed-out enrichment attempt")
	generator := &priorityLibraryGenerator{failure: context.DeadlineExceeded}
	worker, err := enrich.NewService(store, store, generator, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if err := worker.Backfill(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("timeout was hidden", err)
	}
	status := worker.Status()
	if status.Active || status.State != "indexing" || status.Complete || status.LastErrorCode != "timeout" || status.LastErrorAt.IsZero() {
		t.Fatal("retrying backfill lost actual timeout history", status)
	}
	if err := worker.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	status = worker.Status()
	if !status.Complete || status.ProcessedChunks != 1 || status.LastErrorCode != "timeout" {
		t.Fatal("successful retry erased truthful historical timeout", status)
	}
}
