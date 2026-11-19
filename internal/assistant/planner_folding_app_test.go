package assistant

import (
	"testing"
	"time"
)

func TestAccentedKnownAppMatchesFoldedWords(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	apps := []KnownApp{{ID: "com.example.cafecentral", Name: "Café Central", Aliases: []string{"cafe"}}}
	for _, question := range []string{"what did I do in Café Central", "what did I do in cafe central"} {
		t.Run(question, func(t *testing.T) {
			plan, err := PlanQuestion(question, now, apps)
			if err != nil || len(plan.AppIDs) != 1 || plan.AppNames[0] != "Café Central" {
				t.Fatalf("app not matched: %+v err=%v", plan, err)
			}
			for _, term := range plan.Terms {
				if term == "cafe" || term == "central" {
					t.Fatalf("app term leaked into topic terms: %v", plan.Terms)
				}
			}
		})
	}
}

func TestNameMentionsFoldsAccents(t *testing.T) {
	if n := NameMentions("José said hi; jose left", "José"); n != 2 {
		t.Fatalf("NameMentions = %d, want 2", n)
	}
}
