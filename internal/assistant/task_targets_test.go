package assistant

import (
	"context"
	"strings"
	"testing"
)

type taskTargetRetriever struct{ retrieval Retrieval }

func (r taskTargetRetriever) Retrieve(context.Context, string) ([]Evidence, error) {
	return r.retrieval.Evidence, nil
}
func (r taskTargetRetriever) RetrievePlan(context.Context, Plan) (Retrieval, error) {
	return r.retrieval, nil
}

func TestPlannedTaskTargetsReachQuotedAndGeneratedAnswers(t *testing.T) {
	id := strings.Repeat("a", 64)
	target := TargetRef{Kind: "task", ID: strings.Repeat("b", 32), Revision: 7, SourceID: id, Label: "T1"}
	r := taskTargetRetriever{Retrieval{Evidence: []Evidence{{SourceID: id, AppID: "blur.tasks", Text: "Saved task: Pick up the coats\nCurrent state: open", Origin: OriginTask}}, Targets: []TargetRef{target}}}
	for _, generated := range []bool{false, true} {
		var service *Service
		if generated {
			service = NewService(r, &testGenerator{answer: ModelAnswer{Text: "Pick up the coats.", SourceIDs: []string{id}}}, testCounter{tokens: 1}, 8192)
		} else {
			service = NewService(r, nil, nil, 8192)
		}
		result, err := service.Ask(context.Background(), "What do you remember about my coats?")
		if err != nil || len(result.Targets) != 1 || result.Targets[0] != target {
			t.Fatal("planned task action handle lost", generated, result, err)
		}
	}
}

func TestOnlyCitedPlannedTasksBecomeActionTargets(t *testing.T) {
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64)
	r := taskTargetRetriever{Retrieval{Evidence: []Evidence{{SourceID: first, Text: "First task"}, {SourceID: second, Text: "Second task"}}, Targets: []TargetRef{{Kind: "task", ID: strings.Repeat("c", 32), Revision: 1, SourceID: first}, {Kind: "task", ID: strings.Repeat("d", 32), Revision: 1, SourceID: second}}}}
	result, err := NewService(r, &testGenerator{answer: ModelAnswer{Text: "Second task.", SourceIDs: []string{second}}}, testCounter{tokens: 1}, 8192).Ask(context.Background(), "What do you remember about my tasks?")
	if err != nil || len(result.Targets) != 1 || result.Targets[0].SourceID != second || result.Targets[0].Label != "T1" {
		t.Fatal("uncited task became actionable", result, err)
	}
}

func TestSemanticCoverageFailureDoesNotClaimNoSavedMemory(t *testing.T) {
	for _, issue := range []string{"unavailable", "loading", "disabled"} {
		text := EmptyAnswerText(Plan{Kind: KindLookup}, Diagnosis{SemanticIssue: issue})
		if !strings.Contains(text, "Search by meaning") || !strings.Contains(text, "exact") {
			t.Fatal("missing semantic coverage was described as absent memory", issue, text)
		}
		if got := EmptyAnswerText(Plan{Kind: KindCapture}, Diagnosis{SemanticIssue: issue}); strings.Contains(got, "Search by meaning") {
			t.Fatal("semantic state changed capture-listing diagnosis", got)
		}
	}
}
