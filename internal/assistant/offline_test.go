package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type recallRetriever struct {
	evidence []Evidence
	plans    []Plan
	after    func()
	pending  bool
}

func (r *recallRetriever) Retrieve(context.Context, string) ([]Evidence, error) {
	return nil, errors.New("planned retrieval required")
}
func (r *recallRetriever) RetrievePlan(_ context.Context, plan Plan) (Retrieval, error) {
	r.plans = append(r.plans, plan)
	if r.after != nil {
		r.after()
	}
	for _, term := range plan.Terms {
		if term == "mira" {
			return Retrieval{Evidence: r.evidence, Diagnosis: Diagnosis{IndexingPending: r.pending}}, nil
		}
	}
	return Retrieval{Diagnosis: Diagnosis{IndexingPending: r.pending}}, nil
}

func TestIdentityQuestionUsesMatchingLocalMemoryBeforeGeneralModel(t *testing.T) {
	id := strings.Repeat("a", 64)
	r := &recallRetriever{evidence: []Evidence{{SourceID: id, Text: "The rollback owner is Mira."}}}
	g := &testGeneralGenerator{testGenerator: testGenerator{answer: ModelAnswer{Text: "Mira owns rollback.", SourceIDs: []string{id}}}, answer: "An unrelated public figure."}
	result, err := NewService(r, g, testCounter{tokens: 1}, 8192).Ask(context.Background(), "What or who is Mira?")
	if err != nil || result.General || result.Plan.Kind != KindEntity || len(result.Sources) != 1 || len(r.plans) != 1 || g.calls != 0 || g.testGenerator.calls != 1 || !strings.Contains(g.packed.User, "The rollback owner is Mira.") {
		t.Fatal("identity question bypassed matching saved evidence", result, err)
	}
}

func TestMemoryProbeRespectsExplicitGeneralModeAndCreationRequests(t *testing.T) {
	for _, test := range []struct{ question, mode string }{{"Who is Mira?", "general"}, {"Write a poem about Mira", ""}, {"Explain rollback strategies", ""}, {"What is 2+2?", ""}} {
		t.Run(test.question, func(t *testing.T) {
			r := &recallRetriever{evidence: []Evidence{{SourceID: strings.Repeat("a", 64), Text: "Private saved evidence."}}}
			g := &testGeneralGenerator{answer: "A general response."}
			result, err := NewService(r, g, testCounter{tokens: 1}, 8192).AskWithHistoryMode(context.Background(), test.question, nil, test.mode)
			if err != nil || !result.General || len(result.Sources) != 0 || len(r.plans) != 0 || g.calls != 1 || g.testGenerator.calls != 0 {
				t.Fatal("general request used saved evidence", result, r.plans, err)
			}
		})
	}
}

func TestUnmatchedIdentityCanUseGeneralModelWithoutMemory(t *testing.T) {
	r := &recallRetriever{evidence: []Evidence{{SourceID: strings.Repeat("a", 64), Text: "Private Mira evidence."}}}
	g := &testGeneralGenerator{answer: "SQLite is a database engine."}
	result, err := NewService(r, g, testCounter{tokens: 1}, 8192).Ask(context.Background(), "What is SQLite?")
	if err != nil || !result.General || len(result.Sources) != 0 || len(r.plans) != 1 || g.calls != 1 || g.testGenerator.calls != 0 || g.input != "What is SQLite?" {
		t.Fatal("unmatched general question changed its data boundary", result, err)
	}
}

func TestMemoryProbeHonorsCancellationAndPendingCoverage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &recallRetriever{evidence: []Evidence{{SourceID: strings.Repeat("a", 64), Text: "Mira owns rollback."}}, after: cancel}
	if result, err := NewService(r, nil, nil, 8192).Ask(ctx, "Who is Mira?"); !errors.Is(err, context.Canceled) || len(result.Sources) != 0 {
		t.Fatal("cancelled probe published memory", result, err)
	}
	r = &recallRetriever{pending: true}
	result, err := NewService(r, nil, nil, 8192).Ask(context.Background(), "Who is Mira?")
	if err != nil || !result.IndexingPending || !result.Answer.InsufficientEvidence || !strings.Contains(result.Answer.Text, "still being indexed") {
		t.Fatal("pending memory was treated as absent", result, err)
	}
}

func TestOfflineRecallQuotesSelectedPassageAndRetainsOriginal(t *testing.T) {
	prefix := strings.Repeat("unrelated prefix ", 20000)
	tail := "The rollback owner is Mira."
	r := &recallRetriever{evidence: []Evidence{{SourceID: strings.Repeat("a", 64), Text: prefix + tail, MatchStart: len(prefix), MatchEnd: len(prefix) + len(tail), Partial: true}}}
	result, err := NewService(r, nil, nil, 8192).Ask(context.Background(), "What do you remember about Mira?")
	if err != nil || !result.Direct || result.General || result.SentBytes != 0 || result.SentItems != 0 || !strings.Contains(result.Answer.Text, tail) || strings.Contains(result.Answer.Text, "unrelated prefix") || !strings.Contains(result.Answer.Text, "partially captured") || len(result.Answer.Text) > MaxAnswerBytes || result.Truncated != 1 || len(result.Sources) != 1 || result.Sources[0].Text != prefix+tail {
		t.Fatal("offline passage lost bounds or original provenance", result, err)
	}
}

func TestOfflineRecallValidatesUntrustedEvidence(t *testing.T) {
	for _, evidence := range []Evidence{{SourceID: "bad", Text: "Mira"}, {SourceID: strings.Repeat("a", 64), Text: string([]byte{0xff})}, {SourceID: strings.Repeat("a", 64), Text: "Mira", MatchStart: 4, MatchEnd: 8}} {
		r := &recallRetriever{evidence: []Evidence{evidence}}
		if _, err := NewService(r, nil, nil, 8192).Ask(context.Background(), "Find my Mira note"); err == nil {
			t.Fatal("invalid local evidence was displayed", evidence)
		}
	}
}
