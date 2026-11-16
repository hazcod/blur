package assistant

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func sortedTerms(plan Plan) string {
	terms := slices.Clone(plan.Terms)
	slices.Sort(terms)
	return strings.Join(terms, " ")
}

func TestRecencyQuestionsPlanTheLastHourAsCaptures(t *testing.T) {
	loc := brussels(t)
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, loc)
	for _, q := range []string{
		"what was I just working on", "What was I just doing?", "what was I just reading", "what am I working on",
		"what did I just do", "what did I do a moment ago", "what was that just now", "what was I working on just now",
	} {
		t.Run(q, func(t *testing.T) {
			plan, err := PlanQuestion(q, now, nil)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Kind != KindCapture || len(plan.Terms) != 0 {
				t.Fatalf("recency question was not planned as a term-free capture listing: %+v", plan)
			}
			s := plan.Scope
			if s.Label != "just now" || !s.Explicit || !s.Start.Equal(now.Add(-time.Hour)) || !s.End.Equal(now) {
				t.Fatalf("scope = %+v, want the hour ending now labelled just now", s)
			}
		})
	}
}

func TestRecencyQuestionWithATopicStaysATopicLookupInTheLastHour(t *testing.T) {
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.UTC)
	plan, err := PlanQuestion("what did I just read about TLS", now, nil)
	if err != nil || plan.Kind != KindLookup || plan.Scope.Label != "just now" || sortedTerms(plan) != "tls" {
		t.Fatalf("topic recency plan = %+v, err=%v", plan, err)
	}
}

func TestRecencyCuesDoNotScopeOrdinaryQuestions(t *testing.T) {
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.UTC)
	for _, q := range []string{"what is a working set", "what is the justice system", "how do I just in time compile", "what do I just need to install for nginx", "what should I just do about the invoice"} {
		t.Run(q, func(t *testing.T) {
			plan, err := PlanQuestion(q, now, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Scope.IsZero() || plan.Kind == KindCapture {
				t.Fatalf("near miss was scoped as recency: %+v", plan)
			}
		})
	}
	// The older right-now wording keeps its own window.
	plan, _ := PlanQuestion("what am I doing right now", now, nil)
	if plan.Scope.Label != "right now" || plan.Kind != KindRecap {
		t.Fatalf("right now changed: %+v", plan)
	}
}

func TestDayPartsCombineWithDays(t *testing.T) {
	loc := brussels(t)
	now := time.Date(2026, 11, 11, 14, 30, 0, 0, loc) // Wednesday
	at := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, loc) }
	tests := []struct {
		q          string
		kind       Kind
		label      string
		start, end time.Time
	}{
		{"what did I do yesterday afternoon?", KindRecap, "yesterday afternoon", at(10, 12), at(10, 18)},
		{"what did I do yesterday morning", KindRecap, "yesterday morning", at(10, 6), at(10, 12)},
		{"what did I do yesterday evening", KindRecap, "yesterday evening", at(10, 18), at(11, 0)},
		{"what did I do yesterday in the afternoon", KindRecap, "yesterday afternoon", at(10, 12), at(10, 18)},
		{"what did I do this morning", KindRecap, "this morning", at(11, 6), at(11, 12)},
		{"what did I do today in the afternoon", KindRecap, "this afternoon", at(11, 12), at(11, 18)},
		{"what did I do Friday evening", KindRecap, "on Friday evening", at(6, 18), at(7, 0)},
		{"what did I do last Monday morning", KindRecap, "on Monday morning", at(9, 6), at(9, 12)},
		{"what did I do last night", KindRecap, "last night", at(10, 20), at(11, 6)},
		{"what did I do yesterday night", KindRecap, "yesterday night", at(10, 20), at(11, 6)},
		{"what did I do on 8 November in the afternoon", KindRecap, "8 November afternoon", at(8, 12), at(8, 18)},
		{"what did I read on November 9 morning", KindLookup, "9 November morning", at(9, 6), at(9, 12)},
		{"what did I do the afternoon of yesterday", KindRecap, "yesterday afternoon", at(10, 12), at(10, 18)},
	}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			plan, err := PlanQuestion(tt.q, now, nil)
			if err != nil {
				t.Fatal(err)
			}
			s := plan.Scope
			if plan.Kind != tt.kind || s.Label != tt.label || !s.Explicit || !s.Start.Equal(tt.start) || !s.End.Equal(tt.end) {
				t.Fatalf("plan = %+v, want %v %s [%v, %v)", plan, tt.kind, tt.label, tt.start, tt.end)
			}
			for _, term := range plan.Terms {
				switch term {
				case "night", "november", "2026", "8", "9", "11", "09":
					t.Fatalf("scope word leaked into topic terms: %+v", plan.Terms)
				}
			}
		})
	}
}

// Asked on Friday morning, "Friday evening" has not happened yet: it means last
// week's.
func TestWeekdayDayPartThatHasNotStartedMeansLastWeek(t *testing.T) {
	loc := brussels(t)
	now := time.Date(2026, 11, 13, 9, 30, 0, 0, loc) // Friday
	plan, err := PlanQuestion("what did I do Friday evening", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 6, 18, 0, 0, 0, loc)
	if !plan.Scope.Start.Equal(want) || !plan.Scope.End.Equal(want.Add(6*time.Hour)) {
		t.Fatalf("scope = %+v, want the previous Friday evening", plan.Scope)
	}
	plan, _ = PlanQuestion("what did I do Friday morning", now, nil)
	if !plan.Scope.Start.Equal(time.Date(2026, 11, 13, 6, 0, 0, 0, loc)) {
		t.Fatalf("a morning that has begun stays today's: %+v", plan.Scope)
	}
}

func TestDayPartWordsWithoutADayDoNotScope(t *testing.T) {
	now := time.Date(2026, 11, 11, 14, 30, 0, 0, time.UTC)
	for _, q := range []string{"afternoon tea recipe", "best night mode settings", "what is a good evening routine", "how to write a morning script"} {
		t.Run(q, func(t *testing.T) {
			plan, err := PlanQuestion(q, now, nil)
			if err != nil || !plan.Scope.IsZero() {
				t.Fatalf("day-part word scoped without a day: %+v err=%v", plan, err)
			}
		})
	}
}

func TestConversationRecapsPlanAPersonLookup(t *testing.T) {
	loc := brussels(t)
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, loc)
	tests := []struct {
		q      string
		entity string
		terms  string
	}{
		{"recap my convo with Samira", "Samira", "samira"},
		{"Recap my conversation with Samira Peeters", "Samira Peeters", "peeters samira"},
		{"summarize my chat with Dima", "Dima", "dima"},
		{"recap my DMs with samira", "samira", "samira"},
		{"recap the thread with Chloe please", "Chloe", "chloe"},
		{"catch me up on Samira", "Samira", "samira"},
		{"what did Samira and I discuss", "Samira", "samira"},
	}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			plan, err := PlanQuestion(tt.q, now, nil)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Kind != KindLookup || !plan.Conversation || plan.Entity != tt.entity || sortedTerms(plan) != tt.terms {
				t.Fatalf("conversation plan = %+v", plan)
			}
			s := plan.Scope
			if s.Label != "last 30 days" || s.Explicit || !s.Start.Equal(now.AddDate(0, 0, -30)) || !s.End.Equal(now) {
				t.Fatalf("default conversation scope = %+v", s)
			}
		})
	}
	plan, _ := PlanQuestion("recap my chat with Samira yesterday", now, nil)
	if !plan.Conversation || plan.Scope.Label != "yesterday" || !plan.Scope.Explicit {
		t.Fatalf("explicit window was not kept: %+v", plan)
	}
}

func TestDayRecapsAndNearMissesAreNotConversationRecaps(t *testing.T) {
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.UTC)
	for _, q := range []string{"recap my day", "recap today", "recap yesterday", "recap my week", "catch me up on the project", "what is a conversation", "recap my conversation with it"} {
		t.Run(q, func(t *testing.T) {
			plan, err := PlanQuestion(q, now, nil)
			if err != nil || plan.Conversation {
				t.Fatalf("not a person conversation: %+v err=%v", plan, err)
			}
		})
	}
	if plan, _ := PlanQuestion("recap my day", now, nil); plan.Kind != KindRecap {
		t.Fatalf("recap my day must stay a recap: %+v", plan)
	}
}

func TestSpeakerQuestionsSetTheEntityAndKeepTopicTerms(t *testing.T) {
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.UTC)
	tests := []struct {
		q      string
		entity string
		terms  string
	}{
		{"what did Dima say about the pentest retest?", "Dima", "dima pentest retest"},
		{"What did Dima Vermeulen write about the report", "Dima Vermeulen", "dima report vermeulen"},
		{"what did Youssef promise", "Youssef", "promise youssef"},
		{"when did Dima send the scope", "Dima", "dima scope"},
		{"Dima said the retest is scheduled", "Dima", "dima retest scheduled"},
		{"what did Samira ask me", "Samira", "samira"},
	}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			plan, err := PlanQuestion(tt.q, now, nil)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Kind != KindLookup || plan.Entity != tt.entity || sortedTerms(plan) != tt.terms || plan.Conversation {
				t.Fatalf("speaker plan = %+v (terms %q)", plan, sortedTerms(plan))
			}
		})
	}
	for _, q := range []string{"what did I say about the retest", "what did the client say about it", "what did dima say", "what did you say", "He said it was fine"} {
		if plan, _ := PlanQuestion(q, now, nil); plan.Entity != "" {
			t.Fatalf("%q must not name an entity, got %+v", q, plan)
		}
	}
}

func TestStorageQuestionsSeekPersonalMemory(t *testing.T) {
	for _, q := range []string{
		"where is the API key for venice stored again?", "where was the invoice saved", "where is my backup kept",
		"where did I put the licence", "where did we leave the draft", "where is the signing key stored",
		"where is that file again?", "where did I put the SOW draft", "where did I save that invoice",
	} {
		if !QuestionSeeksPersonalMemory(q) {
			t.Errorf("%q should search personal memory", q)
		}
	}
	for _, q := range []string{"where is the Eiffel tower", "where is Paris", "what is the capital of France again?", "where can bears live"} {
		if QuestionSeeksPersonalMemory(q) {
			t.Errorf("%q must remain a general question", q)
		}
	}
}

func TestStorageActQuestionsDropTheVerbFromTheSearchTerms(t *testing.T) {
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.UTC)
	for q, want := range map[string]string{"where did I put the SOW draft": "draft sow", "where did I save that invoice": "invoice"} {
		plan, err := PlanQuestion(q, now, nil)
		if err != nil || plan.Kind != KindLookup || sortedTerms(plan) != want {
			t.Fatalf("%q plan = %+v err=%v, want lookup %q", q, plan, err, want)
		}
	}
}

func TestStorageQuestionPlansALookupFromContentWords(t *testing.T) {
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.UTC)
	plan, err := PlanQuestion("where is the API key for venice stored again?", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != KindLookup || sortedTerms(plan) != "api key venice" || !plan.Scope.IsZero() {
		t.Fatalf("storage question plan = %+v", plan)
	}
}
