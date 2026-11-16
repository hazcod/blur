package client

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

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type noteProcessingExtractor struct {
	started   chan context.Context
	release   chan struct{}
	once      sync.Once
	calls     atomic.Int32
	proposals []notes.Proposal
	err       error
}

func (e *noteProcessingExtractor) Extract(ctx context.Context, _ capture.SanitizedObservation) ([]notes.Proposal, error) {
	e.calls.Add(1)
	e.started <- ctx
	<-e.release // Deliberately ignores cancellation at the provider boundary.
	return append([]notes.Proposal(nil), e.proposals...), e.err
}
func (e *noteProcessingExtractor) unblock() { e.once.Do(func() { close(e.release) }) }

func noteProcessingFixture(t *testing.T) (*Client, *durable.Store, *noteProcessingExtractor, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.db")
	store, err := durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	extractor := &noteProcessingExtractor{started: make(chan context.Context, maxNoteProcessingJobs), release: make(chan struct{}), proposals: []notes.Proposal{{Kind: notes.FollowUp, Text: "Send the revised draft"}}}
	service := notes.NewService(store, extractor, nil)
	controller := &localAIController{path: filepath.Join(dir, "ai.json"), service: service, settings: LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}, secrets: &bootstrapSecrets{values: map[string][]byte{}}}
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(store).WithNotes(service).WithLocalAI(controller).WithDeletion(deleter)
	t.Cleanup(func() { extractor.unblock(); c.Close(); controller.Close(); _ = store.Close() })
	return c, store, extractor, path
}
func waitNoteProcessing(t *testing.T, c *Client, match func(NoteProcessingStatus) bool) NoteProcessingStatus {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		status := c.NoteProcessingStatus()
		if match(status) {
			return status
		}
		select {
		case <-c.NoteProcessingChanges():
		case <-timer.C:
			t.Fatal("note processing did not reach expected bounded status", status)
		}
	}
}

func TestBackgroundNoteSuggestionsPersistAfterReceiptAndReopen(t *testing.T) {
	c, store, extractor, path := noteProcessingFixture(t)
	text := "After the meeting, send the revised draft."
	receipt, err := c.AddNote(text)
	if err != nil || receipt.Processing != notes.ProcessingQueued {
		t.Fatal(receipt, err)
	}
	select {
	case <-extractor.started:
	case <-time.After(time.Second):
		t.Fatal("background extraction did not start")
	}
	date := receipt.CreatedAt.In(time.Local).Format("2006-01-02")
	before, _, err := store.GetTodaySnapshot(context.Background(), date)
	if err != nil || len(before.TodoCandidates) != 0 {
		t.Fatal("receipt claimed optional suggestions before inference", err)
	}
	if _, err := c.AddNote(text); err != nil || extractor.calls.Load() != 1 {
		t.Fatal("pending source duplicated inference", err)
	}
	extractor.unblock()
	waitNoteProcessing(t, c, func(s NoteProcessingStatus) bool { return s.State == "ready" && s.Pending == 0 })
	if _, err := c.AddNote(text); err != nil || extractor.calls.Load() != 1 {
		t.Fatal("completed source duplicated inference", err)
	}
	c.Close()
	c.localAI.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	after, found, err := reopened.GetTodaySnapshot(context.Background(), date)
	if err != nil || !found || len(after.TodoCandidates) != 1 || len(after.ConfirmedTodos) != 0 || after.TodoCandidates[0].SourceIDs[0] != receipt.SourceID {
		t.Fatal("background suggestions lost encrypted source link after reopen", err)
	}
}

func TestBackgroundNoteQueueBoundsIncludeActiveAndKeepSavedOverflowReceipt(t *testing.T) {
	c, store, extractor, _ := noteProcessingFixture(t)
	first, err := c.AddNote("Ordinary note zero")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-extractor.started:
	case <-time.After(time.Second):
		t.Fatal("extractor did not start")
	}
	for i := 1; i < maxNoteProcessingJobs; i++ {
		result, err := c.AddNote(fmt.Sprintf("Ordinary note %d", i))
		if err != nil || result.Processing != notes.ProcessingQueued {
			t.Fatal("queue filled before declared active+pending bound", result, err)
		}
	}
	overflow, err := c.AddNote("Ordinary overflow note")
	status := c.NoteProcessingStatus()
	if err != nil || overflow.SourceID == "" || overflow.Processing != notes.ProcessingSuggestionsUnavailable || status.ErrorCode != "queue_full" || status.Pending != maxNoteProcessingJobs || extractor.calls.Load() != 1 {
		t.Fatal("overflow lost durable receipt or exceeded actual work bound", status, err)
	}
	if _, found, err := store.GetObservation(context.Background(), overflow.SourceID); err != nil || !found {
		t.Fatal("queue saturation discarded saved note", err)
	}
	if again, err := c.AddNote("Ordinary note zero"); err != nil || again.SourceID != first.SourceID || again.Processing != notes.ProcessingQueued || extractor.calls.Load() != 1 {
		t.Fatal("duplicate active source used another queue slot", err)
	}
}

func TestBackgroundNoteProcessingRejectsLateResultsAfterPrivacyOrClose(t *testing.T) {
	for _, operation := range []string{"deletion", "provider disable", "credential replacement", "close"} {
		t.Run(operation, func(t *testing.T) {
			c, store, extractor, _ := noteProcessingFixture(t)
			if operation == "credential replacement" {
				c.localAI.settings = LocalAISettings{Enabled: true, Provider: ProviderClaude, Model: "synthetic-model", RemoteNotesEnabled: true, CredentialRef: strings.Repeat("a", 32)}
			}
			receipt, err := c.AddNote("After our meeting, send the revised draft.")
			if err != nil {
				t.Fatal(err)
			}
			var inference context.Context
			select {
			case inference = <-extractor.started:
			case <-time.After(time.Second):
				t.Fatal("extractor did not start")
			}
			if _, err := c.AddNote("Queued second note must not reach a retired provider."); err != nil {
				t.Fatal(err)
			}
			changed := make(chan error, 1)
			go func() {
				switch operation {
				case "deletion":
					_, err := c.DeleteSource(receipt.SourceID)
					changed <- err
				case "provider disable":
					changed <- c.ConfigureLocalAI(context.Background(), LocalAISettings{})
				case "credential replacement":
					changed <- c.ConfigureAI(context.Background(), c.localAI.Settings(), "replacement-synthetic-key")
				case "close":
					c.Close()
					changed <- nil
				}
			}()
			select {
			case <-inference.Done():
			case <-time.After(time.Second):
				t.Fatal("privacy/lifecycle change did not cancel active inference")
			}
			if operation == "close" {
				select {
				case err := <-changed:
					t.Fatal("Close released store ownership while actual extractor remained active", err)
				default:
				}
			}
			extractor.unblock()
			if err := <-changed; err != nil {
				t.Fatal(err)
			}
			c.Close() // Join the actual worker, including an uncooperative old provider.
			date := receipt.CreatedAt.In(time.Local).Format("2006-01-02")
			snapshot, _, err := store.GetTodaySnapshot(context.Background(), date)
			if err != nil || len(snapshot.TodoCandidates) != 0 || extractor.calls.Load() != 1 {
				t.Fatal("late provider restored suggestions or processed canceled queued source", err)
			}
			_, found, err := store.GetObservation(context.Background(), receipt.SourceID)
			if err != nil || found != (operation != "deletion") {
				t.Fatal("processing undid durable save/deletion", err)
			}
			if c.NoteProcessingStatus().State == "ready" {
				t.Fatal("retired output published ready status")
			}
		})
	}
}

func TestBackgroundNoteProcessingVisibleFailureAndIndependentRemoteConsent(t *testing.T) {
	for _, test := range []struct {
		name, code string
		err        error
		proposals  []notes.Proposal
	}{
		{"budget", "model_budget_exceeded", notes.ErrModelBudget, nil},
		{"backend", "model_unavailable", errors.New("private backend words"), nil},
		{"invalid", "invalid_output", nil, []notes.Proposal{{Kind: "execute", Text: "untrusted"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, store, extractor, _ := noteProcessingFixture(t)
			extractor.err, extractor.proposals = test.err, test.proposals
			extractor.unblock()
			result, err := c.AddNote("Ordinary source remains saved even when suggestions fail.")
			if err != nil || result.SourceID == "" {
				t.Fatal(err)
			}
			status := waitNoteProcessing(t, c, func(s NoteProcessingStatus) bool { return s.State == "unavailable" && s.Pending == 0 })
			if status.ErrorCode != test.code {
				t.Fatal("backend content escaped closed suggestion error", status)
			}
			if _, found, err := store.GetObservation(context.Background(), result.SourceID); err != nil || !found {
				t.Fatal("suggestion failure lost saved note", err)
			}
		})
	}
	for _, provider := range []string{ProviderClaude, ProviderChatGPT, ProviderOpenAICompatible} {
		t.Run(provider, func(t *testing.T) {
			c, _, extractor, _ := noteProcessingFixture(t)
			c.localAI.settings.Provider = provider
			if result, err := c.AddNote("No note consent for remote provider."); err != nil || result.Processing == notes.ProcessingQueued || extractor.calls.Load() != 0 {
				t.Fatal("remote note transmitted without independent consent", err)
			}
			c.localAI.settings.RemoteNotesEnabled = true
			if result, err := c.AddNote("Explicitly permitted remote note."); err != nil || result.Processing != notes.ProcessingQueued {
				t.Fatal("explicit note consent remained a no-op", err)
			}
			select {
			case <-extractor.started:
			case <-time.After(time.Second):
				t.Fatal("consented note did not reach selected extractor")
			}
		})
	}
}

func TestNoteProcessingCompletedCacheIsBounded(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	w := &noteProcessingWorker{client: c, completed: make(map[noteProcessingKey]bool)}
	for i := 0; i < maxCompletedNoteJobs*3; i++ {
		w.finish(noteProcessingJob{key: noteProcessingKey{sourceID: fmt.Sprintf("%064x", i), memoryEpoch: 1, policyEpoch: 1}}, "", true)
	}
	if len(w.completed) != maxCompletedNoteJobs || len(w.order) != maxCompletedNoteJobs {
		t.Fatal("successful source dedup grew without bound")
	}
}

func TestSavedNoteReceiptPrecedesBackgroundTaskSuggestions(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	extractor := &receiptDelayedExtractor{started: make(chan struct{}, 1), release: make(chan struct{})}
	service := notes.NewService(store, extractor, nil)
	controller := &localAIController{service: service, settings: LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}}
	c := NewClient(store).WithNotes(service).WithLocalAI(controller)
	defer func() { close(extractor.release); c.Close(); controller.Close(); _ = store.Close() }()
	receipt, err := c.AddNote("After the meeting, send the revised draft.")
	if err != nil || receipt.SourceID == "" {
		t.Fatal("durable receipt failed", err)
	}
	select {
	case <-extractor.started:
	case <-time.After(time.Second):
		t.Fatal("saved receipt never reached optional background extraction")
	}
	if receipt.Processing != notes.ProcessingQueued {
		t.Fatal("receipt did not distinguish queued work from completed suggestions", receipt.Processing)
	}
}
