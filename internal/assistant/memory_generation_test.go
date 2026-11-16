package assistant

import (
	"context"
	"strings"
	"testing"
)

func TestMemoryGenerationPermissionIsIndependentFromGeneralAnswers(t *testing.T) {
	id := strings.Repeat("a", 64)
	target := TargetRef{Kind: "task", ID: strings.Repeat("b", 32), Revision: 3, SourceID: id, Label: "T1"}
	r := taskTargetRetriever{Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Saved task: Pick up the coats", Origin: OriginTask}}, Targets: []TargetRef{target}}}
	g := &testGeneralGenerator{testGenerator: testGenerator{answer: ModelAnswer{Text: "Pick up the coats.", SourceIDs: []string{id}}}, answer: "A public answer."}
	s := NewService(r, g, testCounter{tokens: 1}, 8192).WithMemoryGeneration(false)
	result, err := s.Ask(context.Background(), "What do you remember about my coats?")
	if err != nil || !result.Direct || result.SentItems != 0 || result.SentBytes != 0 || len(result.Sources) != 1 || len(result.Targets) != 1 || g.testGenerator.calls != 0 || g.calls != 0 {
		t.Fatal("ungranted memory generation sent saved evidence", result, err)
	}
	result, err = s.Ask(context.Background(), "Explain compiler optimization")
	if err != nil || !result.General || result.Answer.Text != "A public answer." || g.calls != 1 || g.testGenerator.calls != 0 || strings.Contains(g.input, "coats") {
		t.Fatal("memory generation policy disabled or contaminated public answer", result, g.input, err)
	}
	s.WithMemoryGeneration(true)
	result, err = s.Ask(context.Background(), "What do you remember about my coats?")
	if err != nil || result.Direct || result.SentItems != 1 || g.testGenerator.calls != 1 {
		t.Fatal("explicit memory generation permission was ignored", result, err)
	}
}
