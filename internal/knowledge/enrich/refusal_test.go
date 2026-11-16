package enrich_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/client"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type refusingLibraryGenerator struct {
	mu            sync.Mutex
	digest        string
	refuseID      string
	calls         map[string]int
	refuseAfter   int
	refuseAll     bool
	beforeRefusal func(context.Context)
}

func (g *refusingLibraryGenerator) Identity(context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.digest, nil
}
func (*refusingLibraryGenerator) CountInputTokens(context.Context, []derive.Evidence) (int, error) {
	return 10, nil
}
func (g *refusingLibraryGenerator) Generate(ctx context.Context, input []derive.Evidence) ([]derive.Proposal, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id := input[0].SourceID
	g.calls[id]++
	if (g.refuseAll || id == g.refuseID) && g.calls[id] > g.refuseAfter {
		if g.beforeRefusal != nil {
			g.beforeRefusal(ctx)
		}
		return nil, model.ErrAppleRefused
	}
	return []derive.Proposal{{Kind: knowledge.Project, Name: "Atlas", Fact: "Atlas has an offline export workflow.", SourceID: id}}, nil
}

func TestRefusedLibrarySourceDoesNotBlockLaterSourcesOrRetryAfterReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	key := securestore.OfflineFixtureKey()
	store, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	observations := []capture.SanitizedObservation{
		source(t, store, "Synthetic source A describes the Atlas project."),
		source(t, store, "Synthetic source B describes the export workflow."),
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].ContentID() < observations[j].ContentID() })
	first, second := observations[0].ContentID(), observations[1].ContentID()
	generator := &refusingLibraryGenerator{digest: "same-model", refuseID: first, calls: map[string]int{}}
	worker, err := enrich.NewService(store, store, generator, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	worker.NotifySource(first)
	worker.NotifySource(second)
	complete(t, worker)
	if generator.calls[first] != 1 || generator.calls[second] != 1 {
		t.Fatal("one refused source blocked or repeated instead of processing the next source")
	}
	progress, found, err := store.GetEnrichmentProgress(ctx, first)
	if err != nil || !found || progress.Complete || progress.Next != 0 || !progress.Declined {
		t.Fatal("refusal falsely claimed full source coverage or lost its receipt", err)
	}
	status := worker.Status()
	if !status.Complete || !status.CoverageLimited || status.ProcessedChunks != 1 || status.Queued != 0 || status.LastErrorCode != "model_refused" || status.LastErrorAt.IsZero() || status.ErrorCode != "" {
		t.Fatal("refusal was hidden or presented as complete semantic coverage", status)
	}
	worker.Wake()
	complete(t, worker)
	if generator.calls[first] != 1 || generator.calls[second] != 1 {
		t.Fatal("same-model repair automatically retried a refused immutable source")
	}
	worker.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	generator = &refusingLibraryGenerator{digest: "same-model", refuseID: first, calls: map[string]int{}}
	worker, err = enrich.NewService(store, store, generator, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	complete(t, worker)
	if len(generator.calls) != 0 {
		t.Fatal("restart automatically retried an unchanged declined source")
	}
	if status := worker.Status(); !status.CoverageLimited || status.ProcessedChunks != 0 || !status.LastErrorAt.IsZero() {
		t.Fatal("restart lost saved partial coverage or invented a fresh refusal", status)
	}
	generator.mu.Lock()
	generator.digest, generator.refuseID = "new-model", ""
	generator.mu.Unlock()
	worker.Wake()
	complete(t, worker)
	if generator.calls[first] != 1 || generator.calls[second] != 1 {
		t.Fatal("a changed model identity could not reconsider old source receipts")
	}
	if progress, found, err := store.GetEnrichmentProgress(ctx, first); err != nil || !found || !progress.Complete || progress.Declined {
		t.Fatal("new-model processing did not replace refusal receipt", err)
	}
	if worker.Status().CoverageLimited {
		t.Fatal("new model retained prior model's partial coverage")
	}
}

func TestRefusedLibraryTailKeepsPrefixAndRawCaptureSearchAndAsk(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	const tail = "Project NEBULA781 has a copper export workflow."
	observation := source(t, store, strings.Repeat("Atlas is an offline export tool. ", 80)+tail)
	g := &refusingLibraryGenerator{digest: "same-model", refuseID: observation.ContentID(), refuseAfter: 1, calls: map[string]int{}}
	worker, err := enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	complete(t, worker)
	progress, found, err := store.GetEnrichmentProgress(ctx, observation.ContentID())
	if err != nil || !found || progress.Complete || !progress.Declined || progress.Next != enrich.ChunkBytes-enrich.ChunkOverlap || len(progress.Entities) != 1 || worker.Status().ProcessedChunks != 1 {
		t.Fatal("declined tail advanced cursor or discarded valid prefix", err)
	}
	saved, _, found, err := store.GetObservationWithCaptureTime(ctx, observation.ContentID())
	if err != nil || !found || saved.Text() != observation.Text() {
		t.Fatal("refusal changed full raw capture", err)
	}
	page, err := store.SearchExact(ctx, search.ExactQuery{Terms: []string{"nebula781"}, MaxRows: search.MaxIndexRows, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != observation.ContentID() {
		t.Fatal("Library refusal blocked raw tail indexing", err)
	}
	c := client.NewClient(store)
	defer c.Close()
	answer, err := c.AskMemory(ctx, "What is NEBULA781?")
	if err != nil || !answer.Direct || answer.SentItems != 0 || !strings.Contains(answer.Answer.Text, tail) || len(answer.Sources) != 1 || answer.Sources[0].SourceID != observation.ContentID() {
		t.Fatal("declined Library source could not be recalled from raw encrypted evidence without inference", err)
	}
	worker.Wake()
	complete(t, worker)
	if g.calls[observation.ContentID()] != 2 {
		t.Fatal("same model retried declined tail")
	}
}

func TestCanceledDeletedOrPolicyClosedRefusalCannotPublishReceipt(t *testing.T) {
	for _, operation := range []string{"cancel", "delete", "policy", "model-change", "invalidate"} {
		t.Run(operation, func(t *testing.T) {
			store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			o := source(t, store, "Synthetic refusal publication fence source.")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := &priorityLibraryPolicy{mode: power.Balanced}
			g := &refusingLibraryGenerator{digest: "same-model", refuseID: o.ContentID(), calls: map[string]int{}}
			worker, err := enrich.NewService(store, store, g, enrich.Options{Policy: p})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Close()
			g.beforeRefusal = func(context.Context) {
				switch operation {
				case "cancel":
					cancel()
				case "delete":
					if err := store.DeleteObservationRecord(context.Background(), o.ContentID()); err != nil {
						t.Fatal(err)
					}
				case "policy":
					p.mode = power.Saver
				case "model-change":
					g.digest = "changed-model"
				case "invalidate":
					worker.Invalidate()
				}
			}
			err = worker.Backfill(ctx)
			if operation == "delete" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("refusal hid cancellation/policy/model change")
			}
			if _, found, err := store.GetEnrichmentProgress(context.Background(), o.ContentID()); err != nil || found {
				t.Fatal("late refusal persisted after authority expired", err)
			}
			if worker.Status().CoverageLimited {
				t.Fatal("uncommitted refusal claimed saved partial coverage")
			}
		})
	}
}

func TestRefusalBudgetCheckpointPersistsPartialCoverage(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	g := &refusingLibraryGenerator{digest: "same-model", calls: map[string]int{}}
	worker, err := enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	complete(t, worker) // A completed directory checkpoint predates the new source.
	g.refuseAll = true
	for i := range 6 {
		o := source(t, store, fmt.Sprintf("Synthetic incremental refusal %d after directory completion.", i))
		worker.NotifySource(o.ContentID())
	}
	if err := worker.Backfill(context.Background()); !errors.Is(err, enrich.ErrMoreWork) {
		t.Fatal("refusal bypassed bounded four-request quantum", err)
	}
	if len(g.calls) != 4 || worker.Status().Queued != 2 || worker.Status().ProcessedChunks != 0 {
		t.Fatal("refusals were unbounded or counted as processed chunks")
	}
	complete(t, worker)
	checkpoint, found, err := store.GetEnrichmentCheckpoint(context.Background())
	if err != nil || !found || !checkpoint.Complete || !checkpoint.CoverageLimited {
		t.Fatal("incremental decline was absent from saved directory coverage", err)
	}
	worker.Close()
	worker, err = enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	complete(t, worker)
	if !worker.Status().CoverageLimited || len(g.calls) != 6 {
		t.Fatal("restart lost terminal incremental decline")
	}
	for _, calls := range g.calls {
		if calls != 1 {
			t.Fatal("same-model repair retried terminal refusal")
		}
	}
}

func TestLibraryCloseJoinsLateRefusalWithoutPublishing(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	o := source(t, store, "Synthetic late refusal during worker close.")
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	g := &refusingLibraryGenerator{digest: "same-model", refuseID: o.ContentID(), calls: map[string]int{}, beforeRefusal: func(ctx context.Context) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release // deliberately ignore cancellation until real provider retirement
	}}
	worker, err := enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	done, closed := make(chan error, 1), make(chan struct{})
	go func() { done <- worker.Backfill(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	go func() { worker.Close(); close(closed) }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel active provider")
	}
	select {
	case <-closed:
		t.Fatal("Close returned before active provider retired")
	default:
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("late refusal hid Close cancellation", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not join retired provider")
	}
	if _, found, err := store.GetEnrichmentProgress(context.Background(), o.ContentID()); err != nil || found {
		t.Fatal("closed worker published late terminal receipt", err)
	}
}
