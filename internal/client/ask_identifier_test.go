package client

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/profile"
)

func TestAskKeepsTechnicalSubjectAcrossCapturedApps(t *testing.T) {
	for _, app := range []struct{ id, name string }{{"com.apple.terminal", "Terminal"}, {"com.tinyspeck.slackmacgap", "Slack"}} {
		t.Run(app.name, func(t *testing.T) {
			c, store, _, _ := singleSearchFixture(t)
			c.localAI = nil
			ctx := context.Background()
			wanted, err := capture.SanitizeObservation(app.id, "", "review_long_search: reviewed multi-month indexed retrieval.", 4096)
			if err != nil {
				t.Fatal(err)
			}
			other, err := capture.SanitizeObservation(app.id, "", "review_threat_consistency_fixes agent work: classified parser errors for 27 minutes.", 4096)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.PutObservationAt(ctx, wanted, time.Now().Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			if err := store.PutObservationAt(ctx, other, time.Now().Add(-30*time.Second)); err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"review_long_time agent work: review of search scheduling.", "review_long_search_v2 agent work: review of a second search task."} {
				decoy, err := capture.SanitizeObservation(app.id, "", text, 4096)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.PutObservationAt(ctx, decoy, time.Now().Add(-10*time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 12; i++ {
				decoy, _ := capture.SanitizeObservation(app.id, "", fmt.Sprintf("review_long_search_v%d agent work: reviewed another search task.", i+3), 4096)
				if err := store.PutObservationAt(ctx, decoy, time.Now().Add(-5*time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			question := "What work did the review_long_search agent do in " + app.name + " today?"
			answer, err := c.AskMemory(ctx, question)
			if err != nil || answer.Answer.InsufficientEvidence || len(answer.Sources) != 1 || answer.Sources[0].SourceID != wanted.ContentID() || strings.Contains(answer.Answer.Text, "parser errors") {
				t.Fatal("technical subject broadened into another agent's work", answer, err)
			}
			history := quickHistoryTurns(appendQuickConversation(nil, question, answer))
			missingFollow, err := c.AskMemoryWithHistory(ctx, "And what did they say about review_missing_search?", history)
			if err != nil || !missingFollow.Answer.InsufficientEvidence || len(missingFollow.Sources) != 0 {
				t.Fatal("pronoun follow-up ignored new technical subject", missingFollow, err)
			}
			yesterday, err := c.AskMemoryWithHistory(ctx, "And yesterday?", history)
			if err != nil || !yesterday.Answer.InsufficientEvidence || len(yesterday.Sources) != 0 {
				t.Fatal("dated follow-up lost technical subject", yesterday, err)
			}
			follow, err := c.AskMemoryWithHistory(ctx, "And the review_threat_consistency_fixes agent?", history)
			if err != nil || follow.Answer.InsufficientEvidence || len(follow.Sources) != 1 || follow.Sources[0].SourceID != other.ContentID() {
				t.Fatal("new technical subject inherited the old agent identity", follow, err)
			}
			answer, err = c.AskMemory(ctx, "What work did the review_missing_search agent do in "+app.name+" today?")
			if err != nil || !answer.Answer.InsufficientEvidence || len(answer.Sources) != 0 {
				t.Fatal("missing technical subject fell back to unrelated recent captures", answer, err)
			}
		})
	}
}

func TestTechnicalSubjectSupportRejectsExtendedLabel(t *testing.T) {
	for _, text := range []string{"review_long_search-v2", "review_long_search.py", "review_long_search/child", "review_long_search_v2"} {
		if technicalSubjectSupport([]string{"review_long_search"}, text) {
			t.Fatal("extended identifier matched", text)
		}
	}
}

func TestTechnicalSubjectFiltersPersonalFactsButRetainsWritingRules(t *testing.T) {
	c, _, _, _ := singleSearchFixture(t)
	ctx := context.Background()
	for _, text := range []string{"review_threat_consistency_fixes agent work: classified parser errors.", "review_long_search reviewed indexed retrieval."} {
		if _, err := c.CreateProfile(ctx, profile.Fact, text, profile.Scope{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.CreateProfile(ctx, profile.WritingRule, "Use short sentences.", profile.Scope{}); err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"review_missing_search", "review_long_search"} {
		p, _ := assistant.PlanQuestion("What work did "+subject+" do today?", time.Now(), nil)
		personal, err := c.PersonalAnswerContext(ctx, p)
		want := 0
		if subject == "review_long_search" {
			want = 1
		}
		if err != nil || len(personal.Facts) != want || len(personal.WritingRules) != 1 {
			t.Fatal("unrelated personal facts admitted or writing rule lost", personal, err)
		}
	}
}

func TestVoiceRecallRequiresTechnicalSubject(t *testing.T) {
	c, store, _, _ := voiceArchiveFixture(t)
	ctx := context.Background()
	_, ids := appendArchiveVoice(t, store, "agent-voice", "Agent discussion", c.AskTime().Add(-time.Hour), []string{"review_threat_consistency_fixes agent work: classified parser errors. Review the long search later."})
	answer, err := c.AskMemory(ctx, "What did the review_long_search agent say in the meeting today?")
	if err != nil || !answer.Answer.InsufficientEvidence || len(answer.Sources) != 0 {
		t.Fatal("voice attributed a different subject", answer, ids, err)
	}
	_, wanted := appendArchiveVoice(t, store, "named-agent-voice", "review_long_search", c.AskTime().Add(-30*time.Minute), []string{"Reviewed multi-month indexed retrieval."})
	answer, err = c.AskMemory(ctx, "What did the review_long_search agent say in the meeting today?")
	if err != nil || answer.Answer.InsufficientEvidence || len(answer.Sources) != 1 || answer.Sources[0].SourceID != wanted[0] {
		t.Fatal("voice lost explicit recording subject", answer, err)
	}
}
