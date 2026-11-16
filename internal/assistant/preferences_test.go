package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type preferencesRetriever struct {
	testRetriever
	preferences PersonalContext
}

func (r *preferencesRetriever) PersonalAnswerContext(context.Context, Plan) (PersonalContext, error) {
	return r.preferences, nil
}

type ruleGeneralGenerator struct {
	testGeneralGenerator
	rules []string
}

func (g *ruleGeneralGenerator) GenerateGeneralWithRules(_ context.Context, question string, rules []string, _ int) (string, error) {
	g.rules = append([]string(nil), rules...)
	g.input = question
	return "A draft in your preferred style.", nil
}

func TestRulesReachLocalGeneralAnswersWithoutUnrelatedPersonalFacts(t *testing.T) {
	r := &preferencesRetriever{preferences: PersonalContext{WritingRules: []string{"Use short sentences."}, Facts: []Evidence{{SourceID: strings.Repeat("a", 64), Text: "My private dog is Pip.", Origin: OriginUserFact}}}}
	g := &ruleGeneralGenerator{}
	s := NewService(r, g, testCounter{tokens: 100}, 8192).WithPersonalContext(true)
	result, err := s.Ask(context.Background(), "Explain a compiler.")
	if err != nil || !result.General || len(g.rules) != 1 || g.input != "Explain a compiler." || r.calls != 0 || len(result.Sources) != 0 {
		t.Fatal(result, g.rules, g.input, err)
	}
	// An existing remote Ask grant must not implicitly add profile data.
	g = &ruleGeneralGenerator{testGeneralGenerator: testGeneralGenerator{answer: "A compiler translates source code."}}
	_, err = NewService(r, g, testCounter{tokens: 100}, 8192).Ask(context.Background(), "Explain a compiler.")
	if err != nil || len(g.rules) != 0 {
		t.Fatal("profile context sent without separate admission", err)
	}
}

func TestExplicitFactsEnterCitedMemoryContextAndRulesRemainSeparate(t *testing.T) {
	id := strings.Repeat("a", 64)
	r := &preferencesRetriever{preferences: PersonalContext{WritingRules: []string{"Be direct."}, Facts: []Evidence{{SourceID: id, Text: "My preferred language is Dutch.", Origin: OriginUserFact}}}}
	g := &testGenerator{answer: ModelAnswer{Text: "You prefer Dutch.", SourceIDs: []string{id}}}
	result, err := NewService(r, g, testCounter{tokens: 100}, 8192).WithPersonalContext(true).Ask(context.Background(), "What is my preferred language?")
	if err != nil || len(result.Sources) != 1 || result.Sources[0].Origin != OriginUserFact {
		t.Fatal(result, err)
	}
	var payload struct {
		WritingRules []string `json:"writing_rules"`
		Evidence     []struct {
			Origin string `json:"origin"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(g.packed.User), &payload); err != nil || len(payload.WritingRules) != 1 || len(payload.Evidence) != 1 || payload.Evidence[0].Origin != "user_fact" {
		t.Fatal(g.packed.User, err)
	}
}

type offlineDirectRetriever struct{ testRetriever }

func (r *offlineDirectRetriever) AnswerDirect(context.Context, Plan) (DirectAnswer, bool, error) {
	return DirectAnswer{Answer: Answer{Text: "Your preference is Dutch."}}, true, nil
}
func TestDirectAnswersDoNotRequireConfiguredModel(t *testing.T) {
	r := &offlineDirectRetriever{}
	got, err := NewService(r, nil, nil, 8192).Ask(context.Background(), "What are my preferences?")
	if err != nil || !got.Direct || got.Answer.Text != "Your preference is Dutch." || r.calls != 0 {
		t.Fatal(got, err)
	}
}

func TestAllPackedSourceLabelsCanBeCited(t *testing.T) {
	ids := []string{}
	for n := 1; n <= 12; n++ {
		ids = append(ids, fmt.Sprintf("%064x", n))
	}
	got, err := resolveModelSourceLabels([]string{"S9", "S10", "S12"}, ids)
	if err != nil || len(got) != 3 || got[1] != ids[9] {
		t.Fatal(got, err)
	}
}

func TestGenerativeRequestsDoNotRequireMemoryMatches(t *testing.T) {
	for _, question := range []string{"Write me an email declining an invitation.", "Explain Go contexts to me.", "Rewrite this message: I will follow up tomorrow."} {
		plan, err := PlanQuestion(question, time.Now(), nil)
		if err != nil || plan.Kind != KindGeneral {
			t.Fatalf("creation request became memory lookup: %+v %v", plan, err)
		}
	}
	for _, question := range []string{"Write a summary of what I did today.", "Draft an update based on my notes.", "Explain what we discussed yesterday."} {
		plan, err := PlanQuestion(question, time.Now(), nil)
		if err != nil || plan.Kind == KindGeneral {
			t.Fatalf("personal evidence request lost retrieval: %+v %v", plan, err)
		}
	}
}

func TestInterpretedPersonalCreationRequestRetainsMemoryRetrieval(t *testing.T) {
	id := strings.Repeat("a", 64)
	r := &testRetriever{evidence: []Evidence{{SourceID: id, Text: "Atlas uses Go."}}}
	g := &testGenerator{answer: ModelAnswer{Text: "Atlas uses Go.", SourceIDs: []string{id}}}
	result, err := NewService(r, g, testCounter{tokens: 100}, 8192).AskWithHistoryMode(context.Background(), "Explain my project Atlas.", nil, "memory")
	if err != nil || result.General || r.calls != 1 || len(result.Sources) != 1 {
		t.Fatal(result, err)
	}
}

func TestMemoryResultReportsOnlyRulesActuallySent(t *testing.T) {
	id := strings.Repeat("a", 64)
	question := strings.TrimSpace("Summarize my Atlas work: " + strings.Repeat("include chronology and decisions; ", 115))
	r := &preferencesRetriever{testRetriever: testRetriever{evidence: []Evidence{{SourceID: id, Text: strings.Repeat("Atlas build notes. ", 300)}}}, preferences: PersonalContext{WritingRules: []string{strings.Repeat("a", 1024), strings.Repeat("b", 1024)}, Omitted: 3}}
	g := &testGenerator{answer: ModelAnswer{Text: "Atlas build notes were saved.", SourceIDs: []string{id}}}
	result, err := NewService(r, g, testCounter{tokens: 100}, 8192).WithPersonalContext(true).AskWithHistoryMode(context.Background(), question, nil, "memory")
	if err != nil {
		t.Fatal(err)
	}
	var payload contextPayload
	if err := json.Unmarshal([]byte(g.packed.User), &payload); err != nil {
		t.Fatal(err)
	}
	if result.AppliedRules != len(payload.WritingRules) || result.PreferencesOmitted != 5-len(payload.WritingRules) || len(result.Sources) != 1 || result.Sources[0].Text != r.evidence[0].Text {
		t.Fatal("displayed admission counts or inspectable full source were incorrect", result)
	}
}
