package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/model"
)

const integrityQuestion = "what integrity emails were we talking about?"

func integrityEvidence() []Evidence {
	return []Evidence{
		{SourceID: strings.Repeat("a", 64), AppID: "com.apple.mail", AppName: "Mail", Text: "Integrity email: Mira asked us to confirm the audit evidence before replying.", CapturedAt: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)},
		{SourceID: strings.Repeat("b", 64), AppID: "com.apple.mail", AppName: "Mail", Text: "Integrity follow-up email: the discussion concerned missing checksums in the shipment report.", CapturedAt: time.Date(2026, 11, 13, 11, 0, 0, 0, time.UTC)},
	}
}
func TestIntegrityEmailInvalidAppleAnswersShowOnlySavedPassages(t *testing.T) {
	for _, invalid := range []string{
		`not JSON`,
		`{"source_ids":["S1"],"insufficient_evidence":false}`,
		`{"answer":"UNVERIFIED_PRIVATE_CLAIM","source_ids":"S1","insufficient_evidence":false}`,
		`{"answer":"UNVERIFIED_PRIVATE_CLAIM","source_ids":["S1"],"execute":true}`,
	} {
		t.Run(invalid, func(t *testing.T) {
			completion := &appleCompletionStub{text: invalid, count: 100}
			g := &OllamaGenerator{client: completion, schema: []byte(appleAnswerSchema)}
			evidence := integrityEvidence()
			result, err := NewService(&testRetriever{evidence: evidence}, g, g, ContextTokenLimit).Ask(context.Background(), integrityQuestion)
			if err != nil || !result.Direct || result.General || result.GenerationIssue != "invalid_response" || len(result.Sources) != 2 || len(completion.requests) != 1 {
				t.Fatalf("invalid response did not recover to saved evidence: direct=%t sources=%d calls=%d err=%v", result.Direct, len(result.Sources), len(completion.requests), err)
			}
			if strings.Contains(result.Answer.Text, "UNVERIFIED_PRIVATE_CLAIM") || !strings.Contains(result.Answer.Text, evidence[0].Text) || !strings.Contains(result.Answer.Text, evidence[1].Text) || !strings.Contains(result.Answer.Text, "model answer could not be verified") {
				t.Fatal("invalid generated text retained or real passages lost")
			}
			for i, id := range result.Answer.SourceIDs {
				if id != evidence[i].SourceID {
					t.Fatal("forged citation survived fallback")
				}
			}
		})
	}
}

func TestModelCitationErrorsCannotInvalidateAnswerOrChooseSources(t *testing.T) {
	for _, cited := range []string{`[]`, `[]`, `["S900"]`, `["forged"]`} {
		completion := &appleCompletionStub{text: `{"answer":"The messages discussed audit evidence and missing shipment checksums.","source_labels":` + cited + `,"insufficient_evidence":false}`}
		g := &OllamaGenerator{client: completion, schema: []byte(appleAnswerSchema)}
		evidence := integrityEvidence()
		result, err := NewService(&testRetriever{evidence: evidence}, g, g, ContextTokenLimit).Ask(context.Background(), integrityQuestion)
		if err != nil || !result.Direct || result.GenerationIssue != GenerationInvalidResponse || strings.Contains(result.Answer.Text, "The messages discussed") || len(result.Sources) != len(evidence) {
			t.Fatalf("invalid or empty evidence labels allowed an answer to claim unrelated sources: result=%+v err=%v", result, err)
		}
		for i := range evidence {
			if result.Sources[i].SourceID != evidence[i].SourceID || result.Answer.SourceIDs[i] != evidence[i].SourceID {
				t.Fatal("model citation values changed the app-owned source mapping")
			}
		}
	}
}

type invalidAnswerGenerator struct {
	err   error
	after func()
	calls int
}

func (g *invalidAnswerGenerator) Generate(context.Context, PackedContext, int) (ModelAnswer, error) {
	g.calls++
	if g.after != nil {
		g.after()
	}
	return ModelAnswer{Text: "UNVERIFIED_PRIVATE_CLAIM", SourceIDs: []string{strings.Repeat("f", 64)}}, g.err
}
func TestInvalidAnswerRecoveryDoesNotHideCancellationOrProviderFailures(t *testing.T) {
	denied := errors.New("synthetic policy revoked")
	for _, expected := range []error{context.Canceled, context.DeadlineExceeded, denied, model.ErrAppleUnavailable, model.ErrAppleRefused, model.ErrAppleBudget, errors.Join(ErrAnswer, context.Canceled), errors.Join(ErrGeneratorResponse, denied)} {
		g := &invalidAnswerGenerator{err: expected}
		result, err := NewService(&testRetriever{evidence: integrityEvidence()}, g, testCounter{tokens: 100}, ContextTokenLimit).Ask(context.Background(), integrityQuestion)
		if !errors.Is(err, expected) || result.Answer.Text != "" || g.calls != 1 {
			t.Fatalf("non-structural error hidden: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &invalidAnswerGenerator{err: ErrGeneratorResponse, after: cancel}
	result, err := NewService(&testRetriever{evidence: integrityEvidence()}, g, testCounter{tokens: 100}, ContextTokenLimit).Ask(ctx, integrityQuestion)
	if !errors.Is(err, context.Canceled) || result.Answer.Text != "" {
		t.Fatalf("canceled invalid response published passages: %v", err)
	}
}
func TestIntegrityEmailQuestionWithoutSourcesDoesNotGenerateOrInvent(t *testing.T) {
	g := &invalidAnswerGenerator{err: ErrGeneratorResponse}
	result, err := NewService(&testRetriever{}, g, testCounter{tokens: 100}, ContextTokenLimit).Ask(context.Background(), integrityQuestion)
	if err != nil || g.calls != 0 || !result.Answer.InsufficientEvidence || len(result.Sources) != 0 || result.GenerationIssue != "" {
		t.Fatalf("missing evidence invented recovery: calls=%d err=%v", g.calls, err)
	}
}

func TestGroundedProviderInvalidResponseCodesRecoverWithoutAnotherInference(t *testing.T) {
	for _, failure := range []error{ErrGeneratorResponse, ErrAnswer, model.ErrAppleResponse, model.ErrOllamaResponse, model.ErrCompatibleResponse, model.ErrAnthropicResponse} {
		g := &invalidAnswerGenerator{err: failure}
		result, err := NewService(&testRetriever{evidence: integrityEvidence()}, g, testCounter{tokens: 100}, ContextTokenLimit).Ask(context.Background(), integrityQuestion)
		if err != nil || !result.Direct || result.GenerationIssue != GenerationInvalidResponse || g.calls != 1 || strings.Contains(result.Answer.Text, "UNVERIFIED_PRIVATE_CLAIM") {
			t.Fatalf("invalid response was not safely recovered: direct=%t calls=%d err=%v", result.Direct, g.calls, err)
		}
	}
}
func TestInvalidAnswerFallbackKeepsBoundsAndCoverage(t *testing.T) {
	evidence := integrityEvidence()
	evidence[0].Text = strings.Repeat("Saved integrity email passage. ", 1000)
	evidence[0].Partial = true
	g := &invalidAnswerGenerator{err: ErrGeneratorResponse}
	result, err := NewService(&testRetriever{evidence: evidence}, g, testCounter{tokens: 100}, ContextTokenLimit).Ask(context.Background(), integrityQuestion)
	if err != nil || !result.CoverageLimited || result.Truncated == 0 || len(result.Answer.Text) > MaxAnswerBytes || len(result.Answer.SourceIDs) == 0 || result.GenerationIssue != GenerationInvalidResponse {
		t.Fatalf("fallback lost bounds or coverage: bytes=%d truncated=%d err=%v", len(result.Answer.Text), result.Truncated, err)
	}
}

type failingIntegrityRetriever struct{ err error }

func (r failingIntegrityRetriever) Retrieve(context.Context, string) ([]Evidence, error) {
	return nil, r.err
}
func TestInvalidAnswerRecoveryNeverHidesRetrievalDeletionError(t *testing.T) {
	erased := errors.New("synthetic source deletion in progress")
	g := &invalidAnswerGenerator{err: ErrGeneratorResponse}
	result, err := NewService(failingIntegrityRetriever{erased}, g, testCounter{tokens: 100}, ContextTokenLimit).Ask(context.Background(), integrityQuestion)
	if !errors.Is(err, erased) || g.calls != 0 || result.Answer.Text != "" || result.GenerationIssue != "" {
		t.Fatalf("erasure error concealed by fallback: calls=%d err=%v", g.calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	counter := &OllamaGenerator{client: &appleCompletionStub{}, schema: []byte(appleAnswerSchema)}
	if _, err := counter.CountMemoryInputTokens(ctx, "system", "malformed"); !errors.Is(err, context.Canceled) {
		t.Fatalf("count hid cancellation behind schema error: %v", err)
	}
}
