package client

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type receiptDelayedExtractor struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

type receiptLibraryGenerator struct {
	calls   atomic.Int32
	started chan context.Context
	release chan struct{}
	once    sync.Once
}

func (*receiptLibraryGenerator) Identity(context.Context) (string, error) {
	return "receipt-fixture", nil
}
func (*receiptLibraryGenerator) CountInputTokens(context.Context, []derive.Evidence) (int, error) {
	return 128, nil
}
func (g *receiptLibraryGenerator) Generate(ctx context.Context, evidence []derive.Evidence) ([]derive.Proposal, error) {
	g.calls.Add(1)
	g.started <- ctx
	<-g.release // Deliberately ignore cancellation; publication must still be fenced.
	return []derive.Proposal{{Kind: knowledge.Project, Name: "Atlas", Fact: "Atlas uses Go.", SourceID: evidence[0].SourceID}}, nil
}
func (g *receiptLibraryGenerator) unblock() { g.once.Do(func() { close(g.release) }) }

func receiptLibraryFixture(t *testing.T) (*Client, *durable.Store, *receiptDelayedExtractor, *receiptLibraryGenerator, *enrich.Service) {
	t.Helper()
	dir := t.TempDir()
	store, err := durable.OpenDurable(filepath.Join(dir, "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	extractor := &receiptDelayedExtractor{started: make(chan struct{}, 1), release: make(chan struct{})}
	service := notes.NewService(store, extractor, nil)
	// Inject the Library worker independently below. Note-task extraction is
	// disabled in this fixture so these tests isolate the Library projection.
	controller := &localAIController{path: filepath.Join(dir, "ai.json"), service: service}
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(store).WithNotes(service).WithLocalAI(controller).WithDeletion(deleter)
	g := &receiptLibraryGenerator{started: make(chan context.Context, 1), release: make(chan struct{})}
	worker, err := enrich.NewService(store, store, g, enrich.Options{Fence: c})
	if err != nil {
		t.Fatal(err)
	}
	controller.libraryWorker = worker
	c.addMemoryChangingHook(controller.InvalidateLibrary)
	t.Cleanup(func() { close(extractor.release); g.unblock(); c.Close(); controller.Close(); _ = store.Close() })
	return c, store, extractor, g, worker
}

func TestDesktopReceiptKeepsAutomaticLibraryWithoutDuplicateInference(t *testing.T) {
	c, store, extractor, generator, worker := receiptLibraryFixture(t)
	receipt, err := c.AddNote("Atlas uses Go.\nTODO: Review Atlas")
	if err != nil || receipt.SourceID == "" || extractor.calls.Load() != 0 {
		t.Fatal("note receipt invoked extractor or did not save", err)
	}
	done := make(chan error, 1)
	go func() { done <- worker.Backfill(context.Background()) }()
	select {
	case <-generator.started:
	case <-time.After(time.Second):
		t.Fatal("saved source did not reach existing Automatic Library")
	}
	generator.unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	page, err := store.ListInferredKnowledge(context.Background(), enrich.PageRequest{Limit: 10})
	if err != nil || len(page.Entities) != 1 || len(page.Entities[0].Facts) != 1 || page.Entities[0].Facts[0].Sources[0].ID != receipt.SourceID {
		t.Fatal("background Library lost source-linked suggestions", err)
	}
	// An identical later save still gets a durable receipt, while completed
	// source progress prevents duplicate Library inference.
	if _, err := c.AddNote("Atlas uses Go.\nTODO: Review Atlas"); err != nil {
		t.Fatal(err)
	}
	if err := worker.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	if extractor.calls.Load() != 0 || generator.calls.Load() != 1 {
		t.Fatal("receipt added duplicate inference", extractor.calls.Load(), generator.calls.Load())
	}
}

func TestReceiptLibraryCannotPublishAfterDeletionDisableOrClose(t *testing.T) {
	for _, operation := range []string{"deletion", "provider disable", "close"} {
		t.Run(operation, func(t *testing.T) {
			c, store, extractor, generator, worker := receiptLibraryFixture(t)
			receipt, err := c.AddNote("Atlas uses Go.")
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- worker.Backfill(context.Background()) }()
			var inference context.Context
			select {
			case inference = <-generator.started:
			case <-time.After(time.Second):
				t.Fatal("Library fixture did not enter delayed inference")
			}
			changed := make(chan error, 1)
			go func() {
				switch operation {
				case "deletion":
					_, err := c.DeleteSource(receipt.SourceID)
					changed <- err
				case "provider disable":
					changed <- c.ConfigureLocalAI(context.Background(), LocalAISettings{})
				case "close":
					c.Close()
					c.localAI.Close() // LocalApp.Close drains both before releasing its store.
					changed <- nil
				}
			}()
			select {
			case <-inference.Done():
			case <-time.After(time.Second):
				t.Fatal("lifecycle/privacy change did not cancel Library inference")
			}
			generator.unblock()
			if err := <-changed; err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatal("canceled Library job claimed successful processing")
			}
			page, err := store.ListInferredKnowledge(context.Background(), enrich.PageRequest{Limit: 10})
			if err != nil || len(page.Entities) != 0 || extractor.calls.Load() != 0 {
				t.Fatal("late result published after lifecycle/privacy change", err)
			}
			_, found, err := store.GetObservation(context.Background(), receipt.SourceID)
			if err != nil || found != (operation != "deletion") {
				t.Fatal("lifecycle change lost saved receipt or resurrected deletion", err)
			}
			if operation == "close" {
				if _, err := c.AddNote("Late note"); !errors.Is(err, ErrClientClosed) {
					t.Fatal("closed client accepted a new save", err)
				}
			}
		})
	}
}

func (e *receiptDelayedExtractor) Extract(context.Context, capture.SanitizedObservation) ([]notes.Proposal, error) {
	e.calls.Add(1)
	e.started <- struct{}{}
	<-e.release
	return []notes.Proposal{{Kind: notes.FollowUp, Text: "Model-only follow-up"}}, nil
}

func TestDesktopNoteReceiptDoesNotWaitForOrDuplicateModelExtraction(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	extractor := &receiptDelayedExtractor{started: make(chan struct{}, 1), release: make(chan struct{})}
	c := NewClient(store).WithNotes(notes.NewService(store, extractor, nil))
	defer func() { close(extractor.release); c.Close(); _ = store.Close() }()
	type response struct {
		result notes.Result
		err    error
	}
	done := make(chan response, 1)
	go func() {
		result, err := c.AddNote("TODO: Review the draft\nThe rollback owner is Mira.")
		done <- response{result, err}
	}()
	var receipt notes.Result
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		receipt = got.result
	case <-extractor.started:
		t.Fatal("desktop note save waited for optional model extraction after durable persistence")
	case <-time.After(time.Second):
		t.Fatal("durable note receipt did not complete")
	}
	if extractor.calls.Load() != 0 || receipt.Processing != notes.ProcessingDeterministic || len(receipt.Proposals) != 1 || receipt.Proposals[0].Text != "Review the draft" {
		t.Fatal("receipt invoked model or lost deterministic suggestions", receipt)
	}
	source, found, err := store.GetObservation(context.Background(), receipt.SourceID)
	if err != nil || !found || source.Text() != "TODO: Review the draft\nThe rollback owner is Mira." {
		t.Fatal("receipt acknowledged an uncommitted source", err)
	}
	snapshot, found, err := store.GetTodaySnapshot(context.Background(), receipt.CreatedAt.In(time.Local).Format("2006-01-02"))
	if err != nil || !found || len(snapshot.TodoCandidates) != 1 || len(snapshot.ConfirmedTodos) != 0 || snapshot.TodoCandidates[0].ID != receipt.Proposals[0].ID {
		t.Fatal("receipt lost persisted unconfirmed deterministic suggestions", err)
	}
}
