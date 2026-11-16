package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type testRetriever struct {
	evidence []Evidence
	calls    int
}

type expandingPlanRetriever struct {
	planTestRetriever
	expanded Retrieval
	queries  []string
	expands  int
}

func (r *expandingPlanRetriever) RetrievePlanWithQueries(_ context.Context, _ Plan, queries []string) (Retrieval, error) {
	r.expands++
	r.queries = append([]string(nil), queries...)
	return r.expanded, nil
}

func (r *testRetriever) Retrieve(context.Context, string) ([]Evidence, error) {
	r.calls++
	return r.evidence, nil
}

type testCounter struct{ tokens int }

type byteCounter struct{}

func (byteCounter) CountInputTokens(_ context.Context, system, user string) (int, error) {
	return len(system) + len(user), nil
}

func TestMemoryPackingUsesConfiguredContextBudget(t *testing.T) {
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	r := &testRetriever{evidence: []Evidence{{SourceID: id, Text: strings.Repeat("A fact. ", 600)}}}
	g := &testGenerator{answer: ModelAnswer{Text: "A fact.", SourceIDs: []string{id}}}
	result, err := NewService(r, g, byteCounter{}, 4000).Ask(context.Background(), "What did I do?")
	if err != nil || g.calls != 1 || result.InputTokens+MaxAnswerTokens+PromptTemplateReserve > 4000 || result.Truncated == 0 {
		t.Fatal("valid memory could not fit the configured model budget", result, err)
	}
}

func (c testCounter) CountInputTokens(context.Context, string, string) (int, error) {
	return c.tokens, nil
}

type testGenerator struct {
	answer  ModelAnswer
	answers []ModelAnswer
	packed  PackedContext
	calls   int
}

func (g *testGenerator) Generate(_ context.Context, packed PackedContext, _ int) (ModelAnswer, error) {
	g.calls++
	g.packed = packed
	if g.calls <= len(g.answers) {
		return g.answers[g.calls-1], nil
	}
	return g.answer, nil
}

func TestAskExpandsRankedAlternateQueriesAfterInsufficientAnswer(t *testing.T) {
	firstID := strings.Repeat("a", 64)
	secondID := strings.Repeat("b", 64)
	retriever := &expandingPlanRetriever{
		planTestRetriever: planTestRetriever{retrieval: Retrieval{
			Evidence:         []Evidence{{SourceID: firstID, Text: "A brief integrity email mentioned a report."}},
			AlternateQueries: []string{"audit evidence", "shipment checksums"},
		}},
		expanded: Retrieval{Evidence: []Evidence{{SourceID: secondID, Text: "The integrity emails concerned missing checksums in the shipment report."}}},
	}
	generator := &testGenerator{answers: []ModelAnswer{
		{InsufficientEvidence: true},
		{Text: "They were about missing checksums in the shipment report.", SourceIDs: []string{secondID}},
	}}
	got, err := NewService(retriever, generator, testCounter{tokens: 100}, 8192).Ask(context.Background(), "What was in the integrity emails?")
	if err != nil || got.Answer.InsufficientEvidence || got.Answer.Text == "" || len(got.Sources) != 1 || got.Sources[0].SourceID != secondID || generator.calls != 2 || retriever.expands != 1 {
		t.Fatalf("Ask did not retry with expanded, cited evidence: result=%+v model_calls=%d expansions=%d err=%v", got, generator.calls, retriever.expands, err)
	}
	if strings.Join(retriever.queries, "|") != "audit evidence|shipment checksums" {
		t.Fatalf("expanded retrieval queries = %q", retriever.queries)
	}
}

func TestAskDoesNotExpandWhenNoAlternateQueriesWereRanked(t *testing.T) {
	retriever := &expandingPlanRetriever{planTestRetriever: planTestRetriever{retrieval: Retrieval{
		Evidence: []Evidence{{SourceID: strings.Repeat("a", 64), Text: "An integrity email mentioned a report."}},
	}}}
	generator := &testGenerator{answer: ModelAnswer{InsufficientEvidence: true}}
	got, err := NewService(retriever, generator, testCounter{tokens: 100}, 8192).Ask(context.Background(), "What was in the integrity emails?")
	if err != nil || !got.Answer.InsufficientEvidence || generator.calls != 1 || retriever.expands != 0 {
		t.Fatalf("Ask expanded without model-ranked queries: result=%+v model_calls=%d expansions=%d err=%v", got, generator.calls, retriever.expands, err)
	}
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

func TestNamedSubjectAttributeWithoutEvidenceAbstainsBeforeGeneration(t *testing.T) {
	id := strings.Repeat("a", 64)
	r := &planTestRetriever{retrieval: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "The rollback owner is Mira."}}}}
	g := &testGenerator{answer: ModelAnswer{Text: "The rollback owner is Mira.", SourceIDs: []string{id}}}
	got, err := NewService(r, g, testCounter{tokens: 100}, 8192).Ask(context.Background(), "What is Mira's deployment deadline?")
	if err != nil || !got.Answer.InsufficientEvidence || g.calls != 0 || strings.Contains(got.Answer.Text, "rollback owner") || !strings.Contains(got.Answer.Text, "Mira") || !strings.Contains(got.Answer.Text, "deployment deadline") {
		t.Fatalf("unrelated fact was allowed to answer an unsupported attribute: result=%+v calls=%d err=%v", got, g.calls, err)
	}
}

func TestNamedSubjectAttributeAcceptsRelatedWording(t *testing.T) {
	id := strings.Repeat("b", 64)
	r := &planTestRetriever{retrieval: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Mira's Aurora rollout is planned for November 20."}}}}
	g := &testGenerator{answer: ModelAnswer{Text: "Mira's rollout is planned for November 20.", SourceIDs: []string{id}}}
	got, err := NewService(r, g, testCounter{tokens: 100}, 8192).Ask(context.Background(), "What is Mira's deployment deadline?")
	if err != nil || got.Answer.InsufficientEvidence || g.calls != 1 || !strings.Contains(got.Answer.Text, "November 20") {
		t.Fatalf("related wording was rejected: result=%+v calls=%d err=%v", got, g.calls, err)
	}
}

func TestNamedSubjectRoleAcceptsOccupationWording(t *testing.T) {
	id := strings.Repeat("c", 64)
	r := &planTestRetriever{retrieval: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Mira leads the infrastructure team."}}}}
	g := &testGenerator{answer: ModelAnswer{Text: "Mira leads the infrastructure team.", SourceIDs: []string{id}}}
	got, err := NewService(r, g, testCounter{tokens: 100}, 8192).Ask(context.Background(), "What is Mira's job?")
	if err != nil || got.Answer.InsufficientEvidence || g.calls != 1 || !strings.Contains(got.Answer.Text, "infrastructure team") {
		t.Fatalf("occupation wording was rejected: result=%+v calls=%d err=%v", got, g.calls, err)
	}
}

func TestNamedSubjectAttributeCannotBorrowAnotherSubjectsValue(t *testing.T) {
	id := strings.Repeat("d", 64)
	r := &planTestRetriever{retrieval: Retrieval{Evidence: []Evidence{{
		SourceID: id,
		Text:     "The deployment deadline for an unrelated project is November 20. The rollback owner is Mira.",
	}}}}
	g := &testGenerator{answer: ModelAnswer{Text: "Mira's deployment deadline is November 20.", SourceIDs: []string{id}}}
	got, err := NewService(r, g, testCounter{tokens: 100}, 8192).Ask(context.Background(), "What is Mira's deployment deadline?")
	if err != nil || !got.Answer.InsufficientEvidence || g.calls != 0 || strings.Contains(got.Answer.Text, "November 20") {
		t.Fatalf("another subject's value was attributed to Mira: result=%+v calls=%d err=%v", got, g.calls, err)
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
	result, err := service.Ask(context.Background(), "What did I do?")
	if err != nil || !result.Direct || result.GenerationIssue != GenerationInvalidResponse || strings.Contains(result.Answer.Text, "A fabricated fact.") || !strings.Contains(result.Answer.Text, "A fact.") || len(result.Sources) != 1 || len(result.Answer.SourceIDs) != 1 || result.Answer.SourceIDs[0] != id || generator.calls != 1 {
		t.Fatalf("invalid generated answer was retained or lost real evidence: direct=%t sources=%d calls=%d err=%v", result.Direct, len(result.Sources), generator.calls, err)
	}
}

func TestOutstandingWorkUsesOnlyStructuredEvidenceWithoutCallingModel(t *testing.T) {
	loopID := strings.Repeat("a", 64)
	taskID := strings.Repeat("b", 64)
	rawID := strings.Repeat("c", 64)
	target := TargetRef{Kind: "task", ID: "task-1", Revision: 3, SourceID: taskID}
	r := &planTestRetriever{retrieval: Retrieval{
		Evidence: []Evidence{
			{SourceID: loopID, Text: "CraftedSignal\n- Open: Confirm the launch date\n- Possible open item (inferred, not confirmed): Consider enabling customer billing", Layer: LayerDigest},
			{SourceID: taskID, Text: "Saved task: Confirm release readiness\nCurrent state: open", Origin: OriginTask},
			{SourceID: rawID, Text: "Sign in to your account. Email address. Remember me.", Layer: LayerSource},
		},
		Targets: []TargetRef{target},
	}}
	g := &testGenerator{}
	svc := NewService(r, g, testCounter{tokens: 1}, 8192)
	got, err := svc.Ask(context.Background(), "What's outstanding for CraftedSignal?")
	if err != nil || !got.Direct || g.calls != 0 || !strings.Contains(got.Answer.Text, "Confirm the launch date") || !strings.Contains(got.Answer.Text, "Confirm release readiness") || !strings.Contains(got.Answer.Text, "Possible") || strings.Contains(got.Answer.Text, "Sign in to your account") || len(got.Sources) != 2 || len(got.Answer.SourceIDs) != 2 || len(got.Targets) != 1 || got.Targets[0].ID != target.ID {
		t.Fatalf("outstanding work used noisy or generated evidence: result=%+v calls=%d err=%v", got, g.calls, err)
	}
}

func TestOutstandingWorkDoesNotFallBackToRawCapture(t *testing.T) {
	rawID := strings.Repeat("d", 64)
	r := &planTestRetriever{retrieval: Retrieval{Evidence: []Evidence{{SourceID: rawID, Text: "Sign in to your account. Email address.", Layer: LayerSource}}}}
	g := &testGenerator{}
	got, err := NewService(r, g, testCounter{tokens: 1}, 8192).Ask(context.Background(), "What's outstanding for CraftedSignal?")
	if err != nil || !got.Answer.InsufficientEvidence || g.calls != 0 || len(got.Sources) != 0 || strings.Contains(got.Answer.Text, "Sign in to your account") || !strings.Contains(got.Answer.Text, "saved open work") {
		t.Fatalf("unstructured capture was presented as open work: result=%+v calls=%d err=%v", got, g.calls, err)
	}
}

func TestOutstandingWorkShowsExplicitlyRelatedProjectMaterialWithoutCallingModel(t *testing.T) {
	projectID := strings.Repeat("e", 64)
	r := &planTestRetriever{retrieval: Retrieval{Evidence: []Evidence{{
		SourceID: projectID, Layer: LayerSource, Origin: OriginRelatedProject,
		Text: "Subject: CraftedSignal rollout\nMira: We are waiting for vendor verification.",
	}}}}
	g := &testGenerator{}
	got, err := NewService(r, g, testCounter{tokens: 1}, 8192).Ask(context.Background(), "What's outstanding for CraftedSignal?")
	if err != nil || !got.Direct || !got.Answer.InsufficientEvidence || g.calls != 0 || len(got.Sources) != 1 || got.Sources[0].SourceID != projectID || !strings.Contains(got.Answer.Text, "related project material") || strings.Contains(got.Answer.Text, "Still open") {
		t.Fatalf("related source was lost or promoted to an open task: result=%+v calls=%d err=%v", got, g.calls, err)
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
