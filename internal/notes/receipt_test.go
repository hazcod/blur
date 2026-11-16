package notes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
)

type receiptFailureRepository struct {
	testRepository
	noteErr, proposalsErr error
}

func (r *receiptFailureRepository) SaveNote(ctx context.Context, source capture.SanitizedObservation, at time.Time) error {
	if r.noteErr != nil {
		return r.noteErr
	}
	return r.testRepository.SaveNote(ctx, source, at)
}

func (r *receiptFailureRepository) SaveProposals(ctx context.Context, id string, proposals []Proposal) error {
	if r.proposalsErr != nil {
		return r.proposalsErr
	}
	return r.testRepository.SaveProposals(ctx, id, proposals)
}

func TestReceiptKeepsSavedSourceWhenOptionalSuggestionsCannotPersist(t *testing.T) {
	failure := errors.New("synthetic proposal persistence failure")
	repo := &receiptFailureRepository{proposalsErr: failure}
	extractor := waitingExtractor{started: make(chan struct{}), ended: make(chan struct{})}
	service := NewService(repo, extractor, nil)
	result, err := service.AddReceipt(context.Background(), "TODO: Review the draft\npassword: synthetic-secret")
	if err != nil || result.SourceID == "" || result.SourceID != repo.note.ContentID() || repo.writeCount != 1 || result.Processing != ProcessingSuggestionsUnavailable {
		t.Fatal("suggestion failure misreported a committed note as unsaved", result, err)
	}
	if repo.note.Text() != "TODO: Review the draft\npassword: [REDACTED:SECRET]" {
		t.Fatal("receipt did not persist sanitized evidence")
	}
	select {
	case <-extractor.started:
		t.Fatal("receipt started optional inference")
	default:
	}
}

func TestReceiptDoesNotAcknowledgeFailedSourcePersistence(t *testing.T) {
	failure := errors.New("synthetic source persistence failure")
	repo := &receiptFailureRepository{noteErr: failure}
	result, err := NewService(repo, nil, nil).AddReceipt(context.Background(), "TODO: Review the draft")
	if !errors.Is(err, failure) || result.SourceID != "" || len(repo.proposals) != 0 {
		t.Fatal("failed source save returned a durable receipt", result, err)
	}
}
