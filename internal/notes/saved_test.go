package notes

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/model"
)

type savedRepository struct {
	mu                         sync.Mutex
	source                     capture.SanitizedObservation
	at                         time.Time
	present                    bool
	noteWrites, proposalWrites int
	proposals                  []Proposal
}

func (r *savedRepository) SaveNote(_ context.Context, source capture.SanitizedObservation, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.source, r.at, r.present = source, at, true
	r.noteWrites++
	return nil
}
func (r *savedRepository) GetObservationWithCaptureTime(_ context.Context, id string) (capture.SanitizedObservation, time.Time, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.source, r.at, r.present && r.source.ContentID() == id, nil
}
func (r *savedRepository) SaveProposals(ctx context.Context, id string, proposals []Proposal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !r.present || r.source.ContentID() != id {
		return ErrSourceUnavailable
	}
	r.proposalWrites++
	r.proposals = append([]Proposal(nil), proposals...)
	return nil
}

type ignoringSavedExtractor struct{ started, release chan struct{} }

func (e ignoringSavedExtractor) Extract(context.Context, capture.SanitizedObservation) ([]Proposal, error) {
	close(e.started)
	<-e.release
	return []Proposal{{Kind: Task, Text: "Send the revised draft"}}, nil
}

func TestProcessSavedNoteUsesOriginalEvidenceAndTimestampWithoutSavingAgain(t *testing.T) {
	repo := &savedRepository{}
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	service := NewService(repo, testExtractor{proposals: []Proposal{{Kind: Task, Text: "Send the revised draft"}}}, func() time.Time { return at })
	receipt, err := service.AddReceipt(context.Background(), "After the meeting, send the revised draft.")
	if err != nil {
		t.Fatal(err)
	}
	processed, err := service.ProcessSavedNote(context.Background(), receipt.SourceID)
	if err != nil || processed.Processing != ProcessingModel || repo.noteWrites != 1 || repo.proposalWrites != 1 || len(processed.Proposals) != 1 || !processed.Proposals[0].CreatedAt.Equal(at) || processed.Proposals[0].SourceID != receipt.SourceID || processed.Proposals[0].Confirmed {
		t.Fatal("saved-source processing changed evidence or lost provenance", processed, err)
	}
}

func TestProcessSavedNoteRejectsIgnoredCancellationDeletionOrRetiredModel(t *testing.T) {
	for _, change := range []string{"cancellation", "deletion", "model retirement", "guard"} {
		t.Run(change, func(t *testing.T) {
			repo := &savedRepository{}
			extractor := ignoringSavedExtractor{started: make(chan struct{}), release: make(chan struct{})}
			service := NewService(repo, extractor, nil)
			receipt, err := service.AddReceipt(context.Background(), "After the meeting, send the revised draft.")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var guards []CommitGuard
				if change == "guard" {
					guards = []CommitGuard{func(func() error) error { return context.Canceled }}
				}
				_, err := service.ProcessSavedNote(ctx, receipt.SourceID, guards...)
				done <- err
			}()
			select {
			case <-extractor.started:
			case <-time.After(time.Second):
				t.Fatal("synthetic processing did not enter extractor")
			}
			switch change {
			case "cancellation":
				cancel()
			case "deletion":
				repo.mu.Lock()
				repo.present = false
				repo.mu.Unlock()
			case "model retirement":
				service.SetExtractor(testExtractor{})
			}
			close(extractor.release)
			err = <-done
			if err == nil || repo.noteWrites != 1 || repo.proposalWrites != 0 {
				t.Fatal("late extraction wrote after invalidation", err)
			}
			if change == "deletion" && !errors.Is(err, ErrSourceUnavailable) {
				t.Fatal("deleted source not distinguished", err)
			}
		})
	}
}

func TestSynchronousAddRejectsRetiredExtractorEvenIfProviderIgnoresCancellation(t *testing.T) {
	repo := &savedRepository{}
	extractor := ignoringSavedExtractor{started: make(chan struct{}), release: make(chan struct{})}
	service := NewService(repo, extractor, nil)
	done := make(chan Result, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := service.Add(context.Background(), "After the meeting, send the revised draft.")
		done <- result
		errCh <- err
	}()
	select {
	case <-extractor.started:
	case <-time.After(time.Second):
		t.Fatal("synthetic extraction did not start")
	}
	service.SetExtractor(nil)
	close(extractor.release)
	result := <-done
	if err := <-errCh; err != nil || result.SourceID == "" || result.Processing != ProcessingUnavailable || len(result.Proposals) != 0 || repo.proposalWrites != 0 || repo.noteWrites != 1 {
		t.Fatal("synchronous Add saved/returned retired model suggestions", result, err)
	}
}

func TestProcessSavedNotePreservesAppleDeferralWithoutRewritingSource(t *testing.T) {
	repo := &savedRepository{}
	service := NewService(repo, preemptedSavedExtractor{}, nil)
	receipt, err := service.AddReceipt(context.Background(), "After the meeting, send the revised draft.")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ProcessSavedNote(context.Background(), receipt.SourceID)
	if !errors.Is(err, model.ErrBackgroundPreempted) || repo.noteWrites != 1 || repo.proposalWrites != 0 {
		t.Fatal("saved-note deferral was swallowed or published", err)
	}
}

type preemptedSavedExtractor struct{}

func (preemptedSavedExtractor) Extract(context.Context, capture.SanitizedObservation) ([]Proposal, error) {
	return nil, model.ErrBackgroundPreempted
}
