package notes

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
)

type testRepository struct {
	note       capture.SanitizedObservation
	createdAt  time.Time
	proposals  []Proposal
	writeCount int
}

func (r *testRepository) SaveNote(_ context.Context, note capture.SanitizedObservation, at time.Time) error {
	r.note, r.createdAt = note, at
	r.writeCount++
	return nil
}
func (r *testRepository) SaveProposals(_ context.Context, _ string, proposals []Proposal) error {
	r.proposals = append([]Proposal(nil), proposals...)
	return nil
}

type testExtractor struct{ proposals []Proposal }

func (e testExtractor) Extract(context.Context, capture.SanitizedObservation) ([]Proposal, error) {
	return append([]Proposal(nil), e.proposals...), nil
}

type failingExtractor struct{ err error }

func (e failingExtractor) Extract(context.Context, capture.SanitizedObservation) ([]Proposal, error) {
	return nil, e.err
}

type waitingExtractor struct {
	started chan struct{}
	ended   chan struct{}
}

func (e waitingExtractor) Extract(ctx context.Context, _ capture.SanitizedObservation) ([]Proposal, error) {
	close(e.started)
	<-ctx.Done()
	close(e.ended)
	return nil, ctx.Err()
}

func TestAddNoteStoresRedactedSourceBeforeProposingUnconfirmedTasks(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	repo := &testRepository{}
	service := NewService(repo, testExtractor{proposals: []Proposal{{Kind: Task, Text: "Send the proposal", Due: "2026-11-06"}}}, func() time.Time { return now })
	result, err := service.Add(context.Background(), "After lunch, send the proposal tomorrow")
	if err != nil {
		t.Fatal(err)
	}
	if repo.writeCount != 1 || result.SourceID == "" || repo.note.ContentID() != result.SourceID {
		t.Fatalf("note was not saved as linked source: %+v", result)
	}
	if len(result.Proposals) != 1 || result.Proposals[0].Confirmed || result.Proposals[0].SourceID != result.SourceID {
		t.Fatalf("proposal was not source-linked and unconfirmed: %+v", result.Proposals)
	}
}

func TestExplicitTodoMarkerWorksWithoutAI(t *testing.T) {
	repo := &testRepository{}
	result, err := NewService(repo, nil, nil).Add(context.Background(), "- [ ] Review the draft")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Proposals) != 1 || result.Proposals[0].Kind != Task || result.Proposals[0].Text != "Review the draft" || result.Proposals[0].Confirmed {
		t.Fatalf("marker was not projected as a candidate: %+v", result.Proposals)
	}
}

func TestOverBudgetModelLeavesSavedNoteAndDeterministicTodo(t *testing.T) {
	repo := &testRepository{}
	text := "TODO: Send the launch plan\n" + strings.Repeat("context ", 1200)
	result, err := NewService(repo, failingExtractor{err: ErrModelBudget}, nil).Add(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	if repo.writeCount != 1 || result.Processing != ProcessingBudget || len(result.Proposals) != 1 || result.Proposals[0].Text != "Send the launch plan" || len(repo.proposals) != 1 {
		t.Fatalf("budget refusal lost the note or deterministic todo: result=%+v writes=%d saved=%+v", result, repo.writeCount, repo.proposals)
	}
}

func TestDisablingLocalModelCancelsInflightNoteRequest(t *testing.T) {
	repo := &testRepository{}
	extractor := waitingExtractor{started: make(chan struct{}), ended: make(chan struct{})}
	service := NewService(repo, extractor, nil)
	resultCh := make(chan Result, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := service.Add(context.Background(), "a private note")
		resultCh <- result
		errCh <- err
	}()
	select {
	case <-extractor.started:
	case <-time.After(time.Second):
		t.Fatal("model request did not start")
	}
	service.SetExtractor(nil)
	select {
	case <-extractor.ended:
	case <-time.After(time.Second):
		t.Fatal("disabling the model did not cancel the active request")
	}
	result := <-resultCh
	if err := <-errCh; err != nil || result.Processing != ProcessingUnavailable || repo.writeCount != 1 {
		t.Fatalf("disable result=%+v err=%v saved=%d", result, err, repo.writeCount)
	}
}

func TestSetExtractorEnablesAndDisablesModelProcessing(t *testing.T) {
	service := NewService(&testRepository{}, nil, nil)
	if service.AIConfigured() {
		t.Fatal("model unexpectedly configured")
	}
	service.SetExtractor(testExtractor{})
	if !service.AIConfigured() {
		t.Fatal("model configuration was not applied")
	}
	service.SetExtractor(nil)
	if service.AIConfigured() {
		t.Fatal("model remained enabled after disable")
	}
}

func TestInvalidModelProposalsDoNotEscapeAndNoteIsStillSaved(t *testing.T) {
	repo := &testRepository{}
	service := NewService(repo, testExtractor{proposals: []Proposal{{Kind: "external_action", Text: "do it"}}}, nil)
	result, err := service.Add(context.Background(), "Please follow up with Sam next week")
	if err != nil {
		t.Fatal(err)
	}
	if repo.writeCount != 1 || len(result.Proposals) != 0 || result.Processing != "invalid_output" {
		t.Fatalf("unsafe output accepted or note lost: result=%+v writes=%d", result, repo.writeCount)
	}
}

func TestOversizedNoteRejectedBeforeStorage(t *testing.T) {
	repo := &testRepository{}
	_, err := NewService(repo, nil, nil).Add(context.Background(), string(make([]byte, MaxNoteBytes+1)))
	if err == nil || repo.writeCount != 0 {
		t.Fatalf("oversized note: err=%v writes=%d", err, repo.writeCount)
	}
}

func TestModelMayAskOneEvidenceLinkedMeetingFollowUpQuestion(t *testing.T) {
	valid := []Proposal{{Kind: Question, Text: "Who agreed to send the revised draft?"}}
	if _, ok := validateModelOutput(valid); !ok {
		t.Fatal("valid clarification question rejected")
	}
	invalid := []Proposal{{Kind: Question, Text: "Who will send it?"}, {Kind: Question, Text: "When is it due?"}}
	if _, ok := validateModelOutput(invalid); ok {
		t.Fatal("multiple questions for one source accepted")
	}
	invalid = []Proposal{{Kind: Question, Text: "Should someone follow up"}}
	if _, ok := validateModelOutput(invalid); ok {
		t.Fatal("non-question accepted as question")
	}
	invalid = []Proposal{{Kind: Question, Text: "Who agreed to send it?", Due: "tomorrow"}}
	if _, ok := validateModelOutput(invalid); ok {
		t.Fatal("task due-date attached to a question")
	}
}

func TestModelQuestionRequiresMeetingContextAndRemainsUnconfirmed(t *testing.T) {
	question := []Proposal{{Kind: Question, Text: "Who agreed to send the revised draft?"}}
	plainRepo := &testRepository{}
	plain, err := NewService(plainRepo, testExtractor{proposals: question}, nil).Add(context.Background(), "We need to send a revised draft.")
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Proposals) != 0 || len(plainRepo.proposals) != 0 {
		t.Fatalf("question surfaced without meeting context: %+v", plain)
	}
	repo := &testRepository{}
	meeting, err := NewService(repo, testExtractor{proposals: question}, nil).Add(context.Background(), "After the meeting with Alex, we discussed the revised draft.")
	if err != nil {
		t.Fatal(err)
	}
	if len(meeting.Proposals) != 1 || meeting.Proposals[0].Kind != Question || meeting.Proposals[0].Confirmed || meeting.Proposals[0].SourceID != meeting.SourceID || len(repo.proposals) != 1 {
		t.Fatalf("meeting question not linked and pending: result=%+v stored=%+v", meeting, repo.proposals)
	}
}
