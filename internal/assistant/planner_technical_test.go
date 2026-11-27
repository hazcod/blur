package assistant

import (
	"slices"
	"testing"
	"time"
)

func TestTechnicalSubjectIsLiteralContentNotQuestionGrammar(t *testing.T) {
	for _, question := range []string{
		"What work did the review_slack_search agent do today?",
		"What was review_slack_search's work today?",
		"What was review_slack_search’s work today?",
	} {
		p, err := PlanQuestion(question, time.Now(), nil)
		if err != nil || !slices.Equal(p.TechnicalSubjects, []string{"review_slack_search"}) || !slices.Equal(p.Terms, []string{"review", "search", "slack"}) || len(p.AppIDs) != 0 || !p.Literal || !p.MatchOnly {
			t.Fatalf("identifier was interpreted as grammar: %+v %v", p, err)
		}
	}
}

func TestTechnicalSubjectSurvivesDatedContinuation(t *testing.T) {
	now := time.Now()
	previous, _ := PlanQuestion("What work did review_long_search do in Terminal today?", now, nil)
	for _, question := range []string{"And yesterday?", "And what work did it do yesterday?", "What output did it produce yesterday?"} {
		current, _ := PlanQuestion(question, now, nil)
		p := current.Inherit(previous)
		if !slices.Equal(p.TechnicalSubjects, previous.TechnicalSubjects) || !slices.Equal(p.Terms, previous.Terms) || !p.Literal || !p.MatchOnly || p.Kind != KindLookup {
			t.Fatalf("dated continuation dropped subject: %+v", p)
		}
	}
}

func TestTechnicalGeneralContinuationKeepsNewName(t *testing.T) {
	previous, _ := PlanQuestion("What work did review_long_search do in Terminal today?", time.Now(), nil)
	for _, question := range []string{"And Mina?", "And what work did Mina do?", "And what work did Mina do with it?"} {
		current, _ := PlanQuestion(question, time.Now(), nil)
		p := followUpPlan(current, previous)
		if len(p.TechnicalSubjects) != 0 || !slices.Contains(p.Terms, "mina") || p.Literal {
			t.Fatalf("new subject replaced by previous identifier: %+v", p)
		}
	}
}

func TestTechnicalAboutPronounContinuation(t *testing.T) {
	previous, _ := PlanQuestion("What work did review_long_search do in Terminal today?", time.Now(), nil)
	for _, question := range []string{"What about its output?", "And what about its output?"} {
		current, _ := PlanQuestion(question, time.Now(), nil)
		p := followUpPlan(current, previous)
		if !slices.Equal(p.TechnicalSubjects, previous.TechnicalSubjects) || !slices.Equal(p.Terms, previous.Terms) || !p.Literal {
			t.Fatalf("pronoun continuation lost technical subject: %+v", p)
		}
	}
}

func TestSearchRetainsTechnicalSubjectTokens(t *testing.T) {
	p, err := PlanSearch("find review_long_search in Terminal", time.Now(), nil)
	if err != nil || !p.Literal || !slices.Equal(p.Terms, []string{"long", "review", "search"}) || !slices.Equal(p.TechnicalSubjects, []string{"review_long_search"}) {
		t.Fatalf("search lost literal identifier: %+v %v", p, err)
	}
}

func TestTechnicalContinuationKeepsExplicitNewTopic(t *testing.T) {
	now := time.Now()
	previous, _ := PlanQuestion("What work did review_long_search do in Terminal today?", now, nil)
	for _, question := range []string{"And what work did Mina do yesterday?", "And what work did Mina do with it yesterday?"} {
		current, _ := PlanQuestion(question, now, nil)
		p := current.Inherit(previous)
		if len(p.TechnicalSubjects) != 0 || !slices.Contains(p.Terms, "mina") {
			t.Fatalf("new subject replaced by previous identifier: %+v", p)
		}
	}
}

func TestTechnicalSubjectMaskDoesNotLeavePrefixSuffix(t *testing.T) {
	p, err := PlanQuestion("Did build_agent mention build_agent_slack today?", time.Now(), nil)
	if err != nil || len(p.AppIDs) != 0 || len(p.TechnicalSubjects) != 2 {
		t.Fatalf("identifier prefix became app grammar: %+v %v", p, err)
	}
}
