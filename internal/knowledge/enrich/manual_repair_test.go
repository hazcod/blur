package enrich_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type repairFailIdentityLibrary struct {
	*refusingLibraryGenerator
	fail bool
}

type repairFailCheckpointLibrary struct {
	*durable.Store
	failRead, failWrite bool
	resets              int
}

func (r *repairFailCheckpointLibrary) GetEnrichmentCheckpoint(ctx context.Context) (enrich.Checkpoint, bool, error) {
	if r.failRead {
		r.failRead = false
		return enrich.Checkpoint{}, false, errRepairIdentity
	}
	return r.Store.GetEnrichmentCheckpoint(ctx)
}

func (r *repairFailCheckpointLibrary) PutEnrichmentCheckpoint(ctx context.Context, c enrich.Checkpoint) error {
	if r.failWrite {
		r.failWrite = false
		return errRepairIdentity
	}
	if c.After == "" && !c.Complete {
		r.resets++
	}
	return r.Store.PutEnrichmentCheckpoint(ctx, c)
}

func TestManualLibraryRepairSurvivesCheckpointAdmissionFailure(t *testing.T) {
	for _, stage := range []string{"read", "persist_reset"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			source(t, store, "Completed source whose manual repair must survive admission errors.")
			sources := &captureWakeLibrarySources{Store: store}
			repository := &repairFailCheckpointLibrary{Store: store}
			g := &refusingLibraryGenerator{digest: "manual-checkpoint-repair", calls: map[string]int{}}
			worker, err := enrich.NewService(sources, repository, g, enrich.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Close()
			complete(t, worker)
			sources.reset()
			repository.resets = 0
			repository.failRead, repository.failWrite = stage == "read", stage == "persist_reset"
			worker.Wake()
			if err := worker.Backfill(ctx); !errors.Is(err, errRepairIdentity) {
				t.Fatal("checkpoint fixture did not fail", err)
			}
			complete(t, worker)
			sources.mu.Lock()
			pages := sources.legacyPages
			sources.mu.Unlock()
			if pages == 0 || repository.resets != 1 {
				t.Fatal("repair must be durably admitted once", pages, repository.resets)
			}
		})
	}
}

var errRepairIdentity = errors.New("synthetic temporary identity failure")

func (g *repairFailIdentityLibrary) Identity(ctx context.Context) (string, error) {
	if g.fail {
		g.fail = false
		return "", errRepairIdentity
	}
	return g.refusingLibraryGenerator.Identity(ctx)
}
func TestManualLibraryRepairSurvivesIdentityFailure(t *testing.T) {
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	source(t, s, "Completed legacy synthetic evidence.")
	sources := &captureWakeLibrarySources{Store: s}
	g := &repairFailIdentityLibrary{refusingLibraryGenerator: &refusingLibraryGenerator{digest: "independent-manual-repair", calls: map[string]int{}}}
	w, err := enrich.NewService(sources, s, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	complete(t, w)
	sources.reset()
	g.fail = true
	w.Wake()
	if err = w.Backfill(ctx); !errors.Is(err, errRepairIdentity) {
		t.Fatal("identity fixture did not fail", err)
	}
	complete(t, w)
	sources.mu.Lock()
	pages := sources.legacyPages
	sources.mu.Unlock()
	if pages == 0 {
		t.Fatal("manual repair lost before checkpoint acceptance after identity failure")
	}
}
