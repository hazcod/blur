package assistant

import (
	"context"
	"errors"
	"testing"
)

type testRetriever struct {
	evidence []Evidence
	calls    int
}

func (r *testRetriever) Retrieve(context.Context, string) ([]Evidence, error) {
	r.calls++
	return r.evidence, nil
}

type testCounter struct{ tokens int }

func (c testCounter) CountInputTokens(context.Context, string, string) (int, error) {
	return c.tokens, nil
}

type testGenerator struct {
	answer ModelAnswer
	packed PackedContext
	calls  int
}

func (g *testGenerator) Generate(_ context.Context, packed PackedContext, _ int) (ModelAnswer, error) {
	g.calls++
	g.packed = packed
	return g.answer, nil
}

type testGeneralGenerator struct {
	testGenerator
	answer string
	input  string
	calls  int
}

func (g *testGeneralGenerator) GenerateGeneral(_ context.Context, question string, _ int) (string, error) {
	g.calls++
	g.input = question
	return g.answer, nil
}

func TestAskUsesQuestionOnlyGeneralGenerationWhenNoMemoryEvidenceExists(t *testing.T) {
	retriever := &testRetriever{evidence: []Evidence{{SourceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Text: "Private capture that must not be sent."}}}
	generator := &testGeneralGenerator{answer: "2 + 2 = 4."}
	service := NewService(retriever, generator, testCounter{tokens: 40}, 8192)

	got, err := service.Ask(context.Background(), "What is 2+2?")
	if err != nil {
		t.Fatal(err)
	}
	if !got.General || got.Answer.Text != generator.answer || len(got.Sources) != 0 {
		t.Fatalf("general answer result = %+v", got)
	}
	if retriever.calls != 0 || generator.calls != 1 || generator.input != "What is 2+2?" || generator.testGenerator.calls != 0 {
		t.Fatalf("unexpected Ask routing: retrieval=%d general=%d input=%q evidence-generation=%d", retriever.calls, generator.calls, generator.input, generator.testGenerator.calls)
	}
}

func TestAskDoesNotGeneralizeMissingPersonalMemory(t *testing.T) {
	retriever := &testRetriever{}
	generator := &testGeneralGenerator{answer: "The launch is Tuesday."}
	service := NewService(retriever, generator, testCounter{tokens: 40}, 8192)

	got, err := service.Ask(context.Background(), "What did Anna say about the Q3 budget?")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Answer.InsufficientEvidence || got.General || retriever.calls != 1 || generator.calls != 0 || generator.testGenerator.calls != 0 {
		t.Fatalf("missing personal memory was guessed: result=%+v general=%d cited=%d", got, generator.calls, generator.testGenerator.calls)
	}
}

func TestAskRetrievesPacksAndValidatesCitedEvidence(t *testing.T) {
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	retriever := &testRetriever{evidence: []Evidence{{SourceID: id, AppID: "com.example.chat", Text: "The team chose Tuesday."}}}
	generator := &testGenerator{answer: ModelAnswer{Text: "The team chose Tuesday.", SourceIDs: []string{id}}}
	service := NewService(retriever, generator, testCounter{tokens: 1400}, 8192)
	got, err := service.Ask(context.Background(), "When did the team choose?")
	if err != nil {
		t.Fatal(err)
	}
	if retriever.calls != 1 || generator.calls != 1 || got.Answer.Text != generator.answer.Text || len(got.Sources) != 1 || got.Sources[0].SourceID != id || got.InputTokens != 1400 {
		t.Fatalf("Ask result or boundary calls invalid: result=%+v retrieve=%d generate=%d", got, retriever.calls, generator.calls)
	}
	if generator.packed.SourceIDs[0] != id || generator.packed.User == "" {
		t.Fatalf("generator did not receive packed evidence: %+v", generator.packed)
	}
}

func TestAskRejectsOverBudgetBeforeCallingGenerator(t *testing.T) {
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	retriever := &testRetriever{evidence: []Evidence{{SourceID: id, Text: "A fact."}}}
	generator := &testGenerator{answer: ModelAnswer{Text: "A fact.", SourceIDs: []string{id}}}
	service := NewService(retriever, generator, testCounter{tokens: 7600}, 8192)
	if _, err := service.Ask(context.Background(), "What did I do?"); !errors.Is(err, ErrTokenBudget) {
		t.Fatalf("over-budget ask error=%v, want ErrTokenBudget", err)
	}
	if generator.calls != 0 {
		t.Fatal("over-budget evidence reached the model")
	}
}

func TestAskRejectsOverflowingTokenCountBeforeCallingGenerator(t *testing.T) {
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	retriever := &testRetriever{evidence: []Evidence{{SourceID: id, Text: "A fact."}}}
	generator := &testGenerator{answer: ModelAnswer{Text: "A fact.", SourceIDs: []string{id}}}
	service := NewService(retriever, generator, testCounter{tokens: int(^uint(0) >> 1)}, 8192)
	if _, err := service.Ask(context.Background(), "What did I do?"); !errors.Is(err, ErrTokenBudget) {
		t.Fatalf("overflowing token count error=%v, want ErrTokenBudget", err)
	}
	if generator.calls != 0 {
		t.Fatal("overflowing token count reached the model")
	}
}

func TestAskRejectsCitationsOutsideRetrievedEvidence(t *testing.T) {
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	retriever := &testRetriever{evidence: []Evidence{{SourceID: id, Text: "A fact."}}}
	generator := &testGenerator{answer: ModelAnswer{Text: "A fabricated fact.", SourceIDs: []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}
	service := NewService(retriever, generator, testCounter{tokens: 100}, 8192)
	if _, err := service.Ask(context.Background(), "What did I do?"); !errors.Is(err, ErrAnswer) {
		t.Fatalf("uncited answer error=%v, want ErrAnswer", err)
	}
}

func TestAskDoesNotRetrieveAfterCancellation(t *testing.T) {
	retriever := &testRetriever{}
	service := NewService(retriever, &testGenerator{}, testCounter{}, 8192)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Ask(ctx, "question"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Ask error=%v, want context.Canceled", err)
	}
	if retriever.calls != 0 {
		t.Fatal("cancelled Ask performed retrieval")
	}
}

func TestAskCannotRaiseHardContextCeiling(t *testing.T) {
	retriever := &testRetriever{evidence: []Evidence{{SourceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Text: "fact"}}}
	service := NewService(retriever, &testGenerator{}, testCounter{tokens: 1}, ContextTokenLimit+1)
	if _, err := service.Ask(context.Background(), "question"); !errors.Is(err, ErrAskUnavailable) {
		t.Fatalf("invalid context ceiling error=%v, want ErrAskUnavailable", err)
	}
	if retriever.calls != 0 {
		t.Fatal("invalid context ceiling reached retrieval")
	}
}

func TestAskRejectsInvalidQuestionBeforeRetrieval(t *testing.T) {
	retriever := &testRetriever{evidence: []Evidence{{SourceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Text: "fact"}}}
	service := NewService(retriever, &testGenerator{}, testCounter{}, 8192)
	if _, err := service.Ask(context.Background(), string(make([]byte, MaxQuestionBytes+1))); !errors.Is(err, ErrContext) {
		t.Fatalf("invalid question error=%v, want ErrContext", err)
	}
	if retriever.calls != 0 {
		t.Fatal("invalid question was sent to retrieval")
	}
}
