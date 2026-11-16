package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestFactualPossessiveQuestionsProbeNamedSubjectWithoutRequiringUnknownAttribute(t *testing.T) {
	for _, test := range []struct {
		question string
		terms    string
		entity   string
	}{
		{"What is Mira's deployment deadline?", "mira", "Mira"},
		{"What’s Mira’s deployment deadline?", "mira", "Mira"},
		{"When is Mira's deployment due?", "mira", "Mira"},
		{"What is Mira Jansen’s deployment deadline?", "jansen mira", "Mira Jansen"},
		{"what is mira's deployment deadline?", "mira", "mira"},
		{"What is Mira's deployment deadline in Slack?", "mira", "Mira"},
	} {
		t.Run(test.question, func(t *testing.T) {
			plan, err := PlanQuestion(test.question, time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC), nil)
			if err != nil {
				t.Fatal(err)
			}
			candidate, ok := memoryLookupCandidate(plan, nil)
			if !ok || !candidate.MatchOnly || strings.Join(candidate.Terms, " ") != test.terms || candidate.Entity != test.entity || candidate.Question != test.question {
				t.Fatalf("named subject probe = %+v, %t; want terms %q, entity %q", candidate, ok, test.terms, test.entity)
			}
			if strings.Contains(test.question, "Slack") && (len(candidate.AppIDs) != 1 || candidate.AppIDs[0] != "com.tinyspeck.slackmacgap") {
				t.Fatalf("named subject lost explicit app scope: %+v", candidate)
			}
		})
	}
}
