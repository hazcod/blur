package client

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

type singleSearchModel struct {
	mu          sync.Mutex
	calls       int
	answerCalls int
	inputs      []string
	answer      assistant.ModelAnswer
	respond     func(context.Context, string, string) (string, error)
}

func (*singleSearchModel) CloseIdleConnections() {}
func (*singleSearchModel) CountInputTokens(context.Context, string, string) (int, error) {
	return 100, nil
}
func (g *singleSearchModel) Generate(context.Context, assistant.PackedContext, int) (assistant.ModelAnswer, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.answerCalls++
	if g.answer.InsufficientEvidence || g.answer.Text != "" {
		return g.answer, nil
	}
	return assistant.ModelAnswer{}, errors.New("unexpected answer generation")
}
func (g *singleSearchModel) GenerateGeneralPrompt(ctx context.Context, system, user string, _ int) (string, error) {
	g.mu.Lock()
	g.calls++
	g.inputs = append(g.inputs, user)
	g.mu.Unlock()
	if g.respond != nil {
		return g.respond(ctx, system, user)
	}
	if strings.Contains(system, "alternate search") {
		return `{"queries":["drycleanres"]}`, nil
	}
	return `{"matches":[{"ref":"S1","score":0.95}]}`, nil
}

func singleSearchFixture(t *testing.T) (*Client, *durable.Store, string, *singleSearchModel) {
	t.Helper()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	o, err := capture.SanitizeObservation("reader.app", "example.test", "Drycleanres on Monday. Pick up the wool coat.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(context.Background(), o, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	g := &singleSearchModel{}
	c := NewClient(s)
	c.localAI = &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}, askModel: g, service: notes.NewService(s, nil, nil)}
	t.Cleanup(c.Close)
	return c, s, o.ContentID(), g
}

func TestSingleSelectedModelFindsSynonymsWithoutEmbeddingSetup(t *testing.T) {
	c, _, id, g := singleSearchFixture(t)
	hits, err := c.SearchMemoryContext(context.Background(), "laundry")
	if err != nil || len(hits) != 1 || hits[0].SourceID != id || g.calls != 2 || c.memoryIndex != nil {
		t.Fatalf("selected AI did not retrieve and rank saved synonym: hits=%+v calls=%d err=%v", hits, g.calls, err)
	}
	if !strings.Contains(g.inputs[1], "wool coat") || strings.Contains(g.inputs[1], id) {
		t.Fatal("ranking did not use labelled saved evidence")
	}
}

func TestAskPartialTermRecallSurvivesAFullExactCandidateSet(t *testing.T) {
	c, store, _, _ := singleSearchFixture(t)
	c.localAI = nil
	ctx := context.Background()
	for i := 0; i < recallCandidates; i++ {
		observation, err := capture.SanitizeObservation("reader.app", "example.test", fmt.Sprintf("Security news discussed an unrelated lunch menu %d.", i), 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, observation, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	relevant, err := capture.SanitizeObservation("reader.app", "example.test", "Security advisory explains the urgent zero-day patch.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, relevant, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	plan := assistant.Plan{Question: "What was in security news?", Terms: []string{"news", "security"}}
	page, err := c.planSearchCandidatesWithExactRanking(ctx, plan, false, nil, 8, recallCandidates, recallCandidates*16, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range page.Hits {
		if hit.SourceID == relevant.ContentID() {
			return
		}
	}
	t.Fatalf("relevant partial-term capture was crowded out by full exact matches: %+v", page.Hits)
}

func TestAskAnswersWhatOrWhoFromAnExactSavedNoteWithoutModelCalls(t *testing.T) {
	c, store, _, g := singleSearchFixture(t)
	added, err := capture.SanitizeObservation("sh.blur.notes", "", "The rollback owner is Mira.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(context.Background(), added, time.Now().UTC().AddDate(0, 0, -100)); err != nil {
		t.Fatal(err)
	}
	result, err := c.AskMemory(context.Background(), "What or who is Mira?")
	if err != nil || !result.Direct || result.General || result.Answer.InsufficientEvidence || len(result.Sources) != 1 || result.Sources[0].SourceID != added.ContentID() || len(result.Answer.SourceIDs) != 1 || result.Answer.SourceIDs[0] != added.ContentID() || !strings.Contains(result.Answer.Text, "The rollback owner is Mira.") || g.calls != 0 || g.answerCalls != 0 {
		t.Fatalf("exact saved-note identity answer = %+v, %v; recall calls=%d answer calls=%d", result, err, g.calls, g.answerCalls)
	}
}

func TestAskRelevanceGateRejectsSingleUnrelatedExpandedCandidate(t *testing.T) {
	c, _, _, g := singleSearchFixture(t)
	g.answer = assistant.ModelAnswer{InsufficientEvidence: true}
	g.respond = func(_ context.Context, system, user string) (string, error) {
		if strings.Contains(system, "For the user's saved-memory question, do both tasks") {
			return `{"queries":["drycleanres"],"matches":[]}`, nil
		}
		if strings.Contains(system, "alternate search") {
			return `{"queries":["drycleanres"]}`, nil
		}
		return `{"matches":[]}`, nil
	}
	plan, err := assistant.PlanQuestion("What do you remember about compiler?", time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := assistant.NewService(c, g, g, assistant.ContextTokenLimit).WithMemoryGeneration(true).AskWithHistoryMode(context.Background(), plan.Question, nil, "memory")
	if err != nil || !result.Answer.InsufficientEvidence || len(result.Sources) != 0 || g.calls != 2 || g.answerCalls != 0 {
		t.Fatalf("Ask relevance gate did not reject an unrelated expanded candidate: result=%+v query_calls=%d answer_calls=%d err=%v", result, g.calls, g.answerCalls, err)
	}
}

func TestAskRanksExactSlackHitsBeforeUsingThemAsEvidence(t *testing.T) {
	c, store, _, g := singleSearchFixture(t)
	ctx := context.Background()
	target, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "#security-news\nCVE-2026-1234 affects Chrome; update to version 132.0.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "Company security news: the office lunch moved to Friday.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range []capture.SanitizedObservation{target, unrelated} {
		if err := store.PutObservationAt(ctx, observation, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	g.respond = func(_ context.Context, system, user string) (string, error) {
		if strings.Contains(system, "alternate search") && strings.Contains(system, "Rank saved evidence") {
			var payload struct {
				Passages []struct {
					Ref  string `json:"ref"`
					Text string `json:"text"`
				} `json:"passages"`
			}
			if err := json.Unmarshal([]byte(user), &payload); err != nil {
				return "", err
			}
			for _, passage := range payload.Passages {
				if strings.Contains(passage.Text, "CVE-2026-1234") {
					return `{"queries":["security advisories","vulnerability reports"],"matches":[{"ref":"` + passage.Ref + `","score":0.98}]}`, nil
				}
			}
			return `{"queries":["security advisories","vulnerability reports"],"matches":[]}`, nil
		}
		if strings.Contains(system, "Rank saved evidence") {
			return `{"matches":[]}`, nil
		}
		return "unexpected model call", errors.New("unexpected model call")
	}
	plan, err := assistant.PlanQuestion("What was in security news?", time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.RetrievePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(retrieval.Evidence) != 1 || retrieval.Evidence[0].SourceID != target.ContentID() || g.calls != 1 {
		t.Fatalf("Ask passed unrelated exact Slack hit into answer evidence: evidence=%+v calls=%d err=%v", retrieval.Evidence, g.calls, err)
	}
}

func TestExpandedAskSearchPreservesRankedPhrasesAndReturnsOnlyRerankedSources(t *testing.T) {
	c, store, _, g := singleSearchFixture(t)
	ctx := context.Background()
	first, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "Security news: the CISA advisory concerns a vulnerable browser patch.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	second, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "CISA advisories recommend applying the browser patch immediately.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range []capture.SanitizedObservation{first, second} {
		if err := store.PutObservationAt(ctx, observation, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	g.respond = func(_ context.Context, system, user string) (string, error) {
		if strings.Contains(system, "alternate search") && strings.Contains(system, "Rank saved evidence") {
			return `{"queries":["CISA advisories","browser vulnerability bulletin"],"matches":[{"ref":"S1","score":0.95}]}`, nil
		}
		if strings.Contains(system, "Rank saved evidence") {
			var payload struct {
				Passages []struct {
					Ref  string `json:"ref"`
					Text string `json:"text"`
				} `json:"passages"`
			}
			if err := json.Unmarshal([]byte(user), &payload); err != nil {
				return "", err
			}
			for _, passage := range payload.Passages {
				if strings.Contains(passage.Text, "CISA advisories") {
					return `{"matches":[{"ref":"` + passage.Ref + `","score":0.97}]}`, nil
				}
			}
			return `{"matches":[]}`, nil
		}
		return "unexpected model call", errors.New("unexpected model call")
	}
	plan, err := assistant.PlanQuestion("What was in security news?", time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := c.RetrievePlan(ctx, plan)
	if err != nil || len(initial.Evidence) != 1 || initial.Evidence[0].SourceID != first.ContentID() || len(initial.AlternateQueries) != 2 {
		t.Fatalf("initial selected search did not retain its alternate phrases: retrieval=%+v calls=%d err=%v", initial, g.calls, err)
	}
	expanded, err := c.RetrievePlanWithQueries(ctx, plan, initial.AlternateQueries)
	if err != nil || len(expanded.Evidence) != 1 || expanded.Evidence[0].SourceID != second.ContentID() || g.calls != 2 {
		t.Fatalf("expanded search did not rerank the alternate-phrase candidates: retrieval=%+v calls=%d err=%v", expanded, g.calls, err)
	}
}

func TestAskFailsClosedWhenExactHitRelevanceCheckFails(t *testing.T) {
	c, store, _, g := singleSearchFixture(t)
	ctx := context.Background()
	observation, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "#security-news\nCVE-2026-1234 affects Chrome; update now.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, observation, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	unrelated, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "Company security news: the office lunch moved to Friday.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, unrelated, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	g.respond = func(_ context.Context, system, _ string) (string, error) {
		if strings.Contains(system, "Rank saved evidence") {
			return "", errors.New("relevance model unavailable")
		}
		return "unexpected answer generation", errors.New("unexpected model call")
	}
	plan, err := assistant.PlanQuestion("What was in security news?", time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.RetrievePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(retrieval.Evidence) != 0 || retrieval.Diagnosis.SemanticIssue != "limited" || g.calls != 1 {
		t.Fatalf("Ask exposed unchecked exact matches after relevance failure: evidence=%+v diagnosis=%+v calls=%d", retrieval.Evidence, retrieval.Diagnosis, g.calls)
	}
}

func TestAskRelevanceChecksSingleExactHitForNaturalLanguageQuestions(t *testing.T) {
	c, store, _, g := singleSearchFixture(t)
	ctx := context.Background()
	noise, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "Security news: the office lunch moved to Friday.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, noise, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	g.respond = func(_ context.Context, system, _ string) (string, error) {
		if strings.Contains(system, "alternate search") && strings.Contains(system, "Rank saved evidence") {
			return `{"queries":["security alert","incident response","vulnerability update"],"matches":[]}`, nil
		}
		if strings.Contains(system, "Rank saved evidence") {
			return `{"matches":[]}`, nil
		}
		return "unexpected model call", errors.New("unexpected model call")
	}
	plan, err := assistant.PlanQuestion("What was in security news?", time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.RetrievePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(retrieval.Evidence) != 0 || g.calls != 2 {
		t.Fatalf("single incidental exact hit bypassed Ask relevance checking: evidence=%+v calls=%d", retrieval.Evidence, g.calls)
	}
}

func TestAskCombinesExactRelevanceAndQueryRewriteBeforeRetrying(t *testing.T) {
	c, store, _, g := singleSearchFixture(t)
	ctx := context.Background()
	noise, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "Security news: the office lunch moved to Friday.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	target, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "CISA advisories covered critical vulnerabilities in Chrome and urged an immediate update.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range []capture.SanitizedObservation{noise, target} {
		if err := store.PutObservationAt(ctx, observation, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	var combinedCalls, expandedRankCalls int
	g.respond = func(_ context.Context, system, user string) (string, error) {
		if strings.Contains(system, "alternate search") && strings.Contains(system, "Rank saved evidence") {
			combinedCalls++
			var payload struct {
				Passages []struct {
					Ref  string `json:"ref"`
					Text string `json:"text"`
				} `json:"passages"`
			}
			if err := json.Unmarshal([]byte(user), &payload); err != nil {
				return "", err
			}
			return `{"queries":["security advisories","CISA vulnerabilities","cyber threat bulletin"],"matches":[]}`, nil
		}
		if strings.Contains(system, "Rank saved evidence") {
			expandedRankCalls++
			var payload struct {
				Passages []struct {
					Ref  string `json:"ref"`
					Text string `json:"text"`
				} `json:"passages"`
			}
			if err := json.Unmarshal([]byte(user), &payload); err != nil {
				return "", err
			}
			for _, passage := range payload.Passages {
				if strings.Contains(passage.Text, "CISA advisories") {
					return `{"matches":[{"ref":"` + passage.Ref + `","score":0.98}]}`, nil
				}
			}
			return `{"matches":[]}`, nil
		}
		if strings.Contains(system, "alternate search") {
			return `{"queries":["security advisories","CISA vulnerabilities","cyber threat bulletin"]}`, nil
		}
		return "unexpected model call", errors.New("unexpected model call")
	}
	plan, err := assistant.PlanQuestion("What was in security news?", time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.RetrievePlan(ctx, plan)
	if err != nil || len(retrieval.Evidence) != 1 || retrieval.Evidence[0].SourceID != target.ContentID() || combinedCalls != 1 || expandedRankCalls != 1 || g.calls != 2 {
		t.Fatalf("Ask did not combine the first relevance and rewrite pass: evidence=%+v combined=%d expanded_rank=%d calls=%d err=%v", retrieval.Evidence, combinedCalls, expandedRankCalls, g.calls, err)
	}
}

func TestSingleModelKeepsExactQueriesFastAndCloudMemoryOptIn(t *testing.T) {
	c, _, id, g := singleSearchFixture(t)
	hits, err := c.SearchMemoryContext(context.Background(), "drycleanres")
	if err != nil || len(hits) != 1 || hits[0].SourceID != id || g.calls != 0 {
		t.Fatal("exact match waited for model inference", hits, g.calls, err)
	}
	c.localAI.settings.Provider = ProviderClaude
	page, err := c.hybridPage(context.Background(), "laundry", search.ExactPage{}, semantic.Query{Limit: 8}, 8)
	if err != nil || len(page.Hits) != 0 || !page.CoverageLimited || g.calls != 0 {
		t.Fatal("cloud search did not preserve saved-memory consent", page, g.calls, err)
	}
}

func TestSingleModelSearchPreservesAppAndTimeFilters(t *testing.T) {
	c, _, _, g := singleSearchFixture(t)
	for _, scope := range []semantic.Query{
		{AppIDs: []string{"different.app"}},
		{Start: time.Now().Add(-48 * time.Hour), End: time.Now().Add(-24 * time.Hour)},
	} {
		page, err := c.hybridPage(context.Background(), "laundry", search.ExactPage{}, scope, 8)
		if err != nil || len(page.Hits) != 0 {
			t.Fatal("query rewrite broadened user scope", page, err)
		}
	}
	if g.calls != 2 {
		t.Fatalf("empty filtered candidates should need expansion only, calls=%d", g.calls)
	}
}

func TestSingleModelSearchDoesNotPublishAfterMemoryDeletion(t *testing.T) {
	c, _, _, g := singleSearchFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	g.respond = func(_ context.Context, _, _ string) (string, error) {
		close(started)
		<-release
		return `{"queries":["drycleanres"]}`, nil
	}
	done := make(chan error, 1)
	go func() { _, err := c.SearchMemoryContext(context.Background(), "laundry"); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("selected model was never consulted")
	}
	c.NotifyMemoryChanging()
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("late generation exposed memory after deletion boundary")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not finish")
	}
	c.NotifyMemoryStable()
}

func TestSingleModelRecallRanksTyposAndRejectsUnrelatedRecentSources(t *testing.T) {
	for _, score := range []string{"0.95", "0.1"} {
		t.Run(score, func(t *testing.T) {
			c, _, id, g := singleSearchFixture(t)
			g.respond = func(_ context.Context, system, _ string) (string, error) {
				if strings.Contains(system, "alternate search") {
					return `{"queries":["drycleaners"]}`, nil
				}
				return `{"matches":[{"ref":"S1","score":` + score + `}]}`, nil
			}
			page, err := c.SearchMemoryPageContext(context.Background(), "collect laundry")
			if err != nil || !page.CoverageLimited || g.calls != 2 {
				t.Fatal(page, g.calls, err)
			}
			if score == "0.95" && (len(page.Hits) != 1 || page.Hits[0].SourceID != id) {
				t.Fatal("typo source was missed", page)
			}
			if score == "0.1" && len(page.Hits) != 0 {
				t.Fatal("irrelevant source was published", page)
			}
		})
	}
}

func TestSingleModelRecallExpandsTechnicalShorthandToCapturedLanguage(t *testing.T) {
	c, store, _, g := singleSearchFixture(t)
	target, err := capture.SanitizeObservation("reader.app", "example.test", "The command line tool builds and queries an encrypted personal memory repository.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(context.Background(), target, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rewriteCalled := false
	g.respond = func(_ context.Context, system, user string) (string, error) {
		if strings.Contains(system, "alternate search") {
			rewriteCalled = true
			if !strings.Contains(system, "CLI repo") || !strings.Contains(system, "command-line tool") || !strings.Contains(system, "security news") {
				t.Fatalf("recall rewrite prompt omitted real shorthand examples: %q", system)
			}
			var payload struct {
				Passages []struct {
					Ref  string `json:"ref"`
					Text string `json:"text"`
				} `json:"passages"`
			}
			if err := json.Unmarshal([]byte(user), &payload); err != nil {
				return "", err
			}
			for _, passage := range payload.Passages {
				if strings.Contains(passage.Text, "command line tool") {
					return `{"queries":["command line interface repository","command line tool","encrypted memory project"],"matches":[{"ref":"` + passage.Ref + `","score":0.98}]}`, nil
				}
			}
			return `{"queries":["command line interface repository","command line tool","encrypted memory project"],"matches":[]}`, nil
		}
		return "unexpected model call", errors.New("unexpected model call")
	}
	page, err := c.SearchMemoryPageContext(context.Background(), "What is the CLI repo about?")
	if err != nil || !rewriteCalled || len(page.Hits) != 1 || page.Hits[0].SourceID != target.ContentID() || g.calls != 1 {
		t.Fatalf("technical shorthand did not retrieve the matching saved source: page=%+v rewrite=%t calls=%d err=%v", page, rewriteCalled, g.calls, err)
	}
}

func TestAskAnswersBroadTechnicalSubjectQuestionFromItsSavedHistory(t *testing.T) {
	c, store, _, g := singleSearchFixture(t)
	question := "What is the CLI repo about?"
	target, err := capture.SanitizeObservation("reader.app", "example.test", "The command line tool builds and queries an encrypted personal memory repository.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(context.Background(), target, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	g.answer = assistant.ModelAnswer{Text: "The saved source describes a command line tool for encrypted personal memory.", SourceIDs: []string{target.ContentID()}}
	g.respond = func(_ context.Context, system, user string) (string, error) {
		if strings.Contains(system, "For the user's saved-memory question, do both tasks") {
			var payload struct {
				Passages []struct {
					Ref  string `json:"ref"`
					Text string `json:"text"`
				} `json:"passages"`
			}
			if err := json.Unmarshal([]byte(user), &payload); err != nil {
				return "", err
			}
			for _, passage := range payload.Passages {
				if strings.Contains(passage.Text, "command line tool") {
					return `{"queries":["command-line interface repository","command line tool","repository overview"],"matches":[{"ref":"` + passage.Ref + `","score":0.98}]}`, nil
				}
			}
			return `{"queries":["command-line interface repository","command line tool","repository overview"],"matches":[]}`, nil
		}
		if strings.Contains(system, "alternate search") {
			return `{"queries":["command-line interface repository","command line tool","repository overview"]}`, nil
		}
		if strings.Contains(system, "Rank saved evidence") {
			return `{"matches":[{"ref":"S1","score":0.98}]}`, nil
		}
		return "unexpected selected-model call", errors.New("unexpected selected-model call")
	}
	result, err := c.AskMemory(context.Background(), question)
	if err != nil || result.Answer.InsufficientEvidence || !strings.Contains(result.Answer.Text, "command line tool") || len(result.Sources) != 1 || result.Sources[0].SourceID != target.ContentID() || g.calls != 1 || g.answerCalls != 1 {
		t.Fatalf("native Ask failed to answer from the relevant historical source in one recall pass: result=%+v calls=%d/%d err=%v", result, g.calls, g.answerCalls, err)
	}
}

func TestSingleModelRecallRetriesAfterAnIncidentalExactHitIsRejected(t *testing.T) {
	c, store, _, g := singleSearchFixture(t)
	noise, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "Security news: the office lunch moved to Friday.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	target, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "The CISA advisories listed critical vulnerabilities in Chrome and urged an immediate update.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range []capture.SanitizedObservation{noise, target} {
		if err := store.PutObservationAt(context.Background(), observation, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	var rewriteCalls, rankCalls int
	g.respond = func(_ context.Context, system, user string) (string, error) {
		if strings.Contains(system, "alternate search") && strings.Contains(system, "Rank saved evidence") {
			rewriteCalls++
			return `{"queries":["security advisories","CISA vulnerabilities","cyber threat bulletin"],"matches":[]}`, nil
		}
		if strings.Contains(system, "Rank saved evidence") {
			rankCalls++
			var payload struct {
				Passages []struct {
					Ref  string `json:"ref"`
					Text string `json:"text"`
				} `json:"passages"`
			}
			if err := json.Unmarshal([]byte(user), &payload); err != nil {
				return "", err
			}
			for _, passage := range payload.Passages {
				if strings.Contains(passage.Text, "office lunch") {
					return `{"matches":[]}`, nil
				}
				if strings.Contains(passage.Text, "CISA advisories") {
					return `{"matches":[{"ref":"` + passage.Ref + `","score":0.98}]}`, nil
				}
			}
			return `{"matches":[]}`, nil
		}
		return "unexpected model call", errors.New("unexpected model call")
	}
	plan, err := assistant.PlanQuestion("What was in security news?", time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.RetrievePlan(context.Background(), plan)
	if err != nil || len(retrieval.Evidence) != 1 || retrieval.Evidence[0].SourceID != target.ContentID() || rewriteCalls != 1 || rankCalls != 1 || g.calls != 2 {
		t.Fatalf("rejected exact hit prevented alternate Ask recall: evidence=%+v rewrites=%d ranks=%d calls=%d err=%v", retrieval.Evidence, rewriteCalls, rankCalls, g.calls, err)
	}
}

func TestAskKeepsRecentContentAlongsideIncidentalRewriteHits(t *testing.T) {
	c, store, _, g := singleSearchFixture(t)
	ctx := context.Background()
	noise, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "Security advisories: the office lunch moved to Friday.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	target, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "#security-news\nCVE-2026-1234 affects Chrome; install the patch immediately.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.PutObservationAt(ctx, noise, now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, target, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var rankCalls int
	g.respond = func(_ context.Context, system, user string) (string, error) {
		if strings.Contains(system, "alternate search") && strings.Contains(system, "Rank saved evidence") {
			return `{"queries":["security advisories","CISA vulnerabilities","cyber threat bulletin"],"matches":[]}`, nil
		}
		if strings.Contains(system, "Rank saved evidence") {
			rankCalls++
			var payload struct {
				Passages []struct {
					Ref  string `json:"ref"`
					Text string `json:"text"`
				} `json:"passages"`
			}
			if err := json.Unmarshal([]byte(user), &payload); err != nil {
				return "", err
			}
			for _, passage := range payload.Passages {
				if strings.Contains(passage.Text, "CVE-2026-1234") {
					return `{"matches":[{"ref":"` + passage.Ref + `","score":0.98}]}`, nil
				}
			}
			return `{"matches":[]}`, nil
		}
		return "unexpected model call", errors.New("unexpected model call")
	}
	plan, err := assistant.PlanQuestion("What was in security news?", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.RetrievePlan(ctx, plan)
	if err != nil || len(retrieval.Evidence) != 1 || retrieval.Evidence[0].SourceID != target.ContentID() || rankCalls != 1 {
		t.Fatalf("incidental rewrite hit hid recent topical capture: evidence=%+v rank_calls=%d err=%v", retrieval.Evidence, rankCalls, err)
	}
}

func TestAskUsesOneModelPassForRecentSemanticRecall(t *testing.T) {
	c, store, _, g := singleSearchFixture(t)
	ctx := context.Background()
	target, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "#infosec\nCVE-2026-1234 affects Chrome; administrators should install the patch immediately.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.PutObservationAt(ctx, target, now); err != nil {
		t.Fatal(err)
	}
	g.respond = func(_ context.Context, system, user string) (string, error) {
		if !strings.Contains(system, "For the user's saved-memory question, do both tasks") {
			return "unexpected extra recall pass", errors.New("unexpected extra recall pass")
		}
		var payload struct {
			Passages []struct {
				Ref  string `json:"ref"`
				Text string `json:"text"`
			} `json:"passages"`
		}
		if err := json.Unmarshal([]byte(user), &payload); err != nil {
			return "", err
		}
		for _, passage := range payload.Passages {
			if strings.Contains(passage.Text, "CVE-2026-1234") {
				return `{"queries":["security advisories","vulnerability reports"],"matches":[{"ref":"` + passage.Ref + `","score":0.98}]}`, nil
			}
		}
		return `{"queries":["security advisories","vulnerability reports"],"matches":[]}`, nil
	}
	plan, err := assistant.PlanQuestion("What did the security desk flag?", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.RetrievePlan(ctx, plan)
	if err != nil || len(retrieval.Evidence) != 1 || retrieval.Evidence[0].SourceID != target.ContentID() || g.calls != 1 {
		t.Fatalf("recent semantic recall was not one-pass: evidence=%+v calls=%d err=%v", retrieval.Evidence, g.calls, err)
	}
}

func TestAskRanksRecentCaptureAlongsideIncidentalExactMatches(t *testing.T) {
	c, store, _, g := singleSearchFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 24; i++ {
		noise, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", fmt.Sprintf("Security news: office lunch moved to Friday, item %02d.", i), 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, noise, now.Add(-time.Duration(24-i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	target, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "#security-news\nCISA flagged a Chrome zero-day and asked administrators to patch immediately.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, target, now); err != nil {
		t.Fatal(err)
	}
	g.answer = assistant.ModelAnswer{Text: "CISA flagged a Chrome zero-day and advised an immediate patch.", SourceIDs: []string{target.ContentID()}}
	g.respond = func(_ context.Context, system, user string) (string, error) {
		if !strings.Contains(system, "For the user's saved-memory question, do both tasks") {
			return "unexpected second recall pass", errors.New("unexpected second recall pass")
		}
		var payload struct {
			Passages []struct {
				Ref  string `json:"ref"`
				Text string `json:"text"`
			} `json:"passages"`
		}
		if err := json.Unmarshal([]byte(user), &payload); err != nil {
			return "", err
		}
		for _, passage := range payload.Passages {
			if strings.Contains(passage.Text, "CISA flagged a Chrome zero-day") {
				return `{"queries":["security advisories","Chrome zero-day","urgent browser patch"],"matches":[{"ref":"` + passage.Ref + `","score":0.98}]}`, nil
			}
		}
		return `{"queries":["security advisories","Chrome zero-day","urgent browser patch"],"matches":[]}`, nil
	}
	result, err := c.AskMemory(ctx, "What was in security news?")
	if err != nil || result.Answer.InsufficientEvidence || len(result.Sources) != 1 || result.Sources[0].SourceID != target.ContentID() || g.calls != 1 || g.answerCalls != 1 {
		t.Fatalf("recent topical capture was crowded out by incidental exact matches: result=%+v calls=%d/%d err=%v", result, g.calls, g.answerCalls, err)
	}
}

func TestAskExpandsOlderHistoryAfterRecentRecallMiss(t *testing.T) {
	c, store, _, g := singleSearchFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	older, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "#secops\nChrome zero-day advisory urged administrators to patch version 132 immediately.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, older, now.AddDate(0, 0, -100)); err != nil {
		t.Fatal(err)
	}
	var combinedCalls, rankingCalls int
	g.respond = func(_ context.Context, system, user string) (string, error) {
		if strings.Contains(system, "For the user's saved-memory question, do both tasks") {
			combinedCalls++
			return `{"queries":["Chrome zero-day","security advisory","browser patch"],"matches":[]}`, nil
		}
		if strings.Contains(system, "Rank saved evidence") {
			rankingCalls++
			var payload struct {
				Passages []struct {
					Ref  string `json:"ref"`
					Text string `json:"text"`
				} `json:"passages"`
			}
			if err := json.Unmarshal([]byte(user), &payload); err != nil {
				return "", err
			}
			for _, passage := range payload.Passages {
				if strings.Contains(passage.Text, "Chrome zero-day") {
					return `{"matches":[{"ref":"` + passage.Ref + `","score":0.98}]}`, nil
				}
			}
			return `{"matches":[]}`, nil
		}
		return "unexpected recall prompt", errors.New("unexpected recall prompt")
	}
	plan, err := assistant.PlanQuestion("What did I read about browser vulnerabilities?", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.RetrievePlan(ctx, plan)
	if err != nil || len(retrieval.Evidence) != 1 || retrieval.Evidence[0].SourceID != older.ContentID() || combinedCalls != 1 || rankingCalls != 1 {
		t.Fatalf("older history was not searched after the recent miss: evidence=%+v combined=%d ranked=%d err=%v", retrieval.Evidence, combinedCalls, rankingCalls, err)
	}
}

func TestSingleModelRecallFailsClosedForMalformedModelOutput(t *testing.T) {
	for _, output := range []string{
		`{"queries":["drycleanres"],"extra":true}`, `{"queries":null}`, `{"queries":["drycleanres"]} trailing`, `{"Queries":["drycleanres"]}`,
		`{"matches":[{"ref":"S99","score":0.95}]}`, `{"matches":[{"ref":"S1","score":0.95},{"ref":"S1","score":0.8}]}`,
		`{"matches":[{"ref":"S1"}]}`, `{"matches":[{"ref":"S1","score":2}]}`, `{"matches":[{"ref":"S1","score":0.9,"extra":true}]}`,
	} {
		t.Run(output, func(t *testing.T) {
			c, _, _, g := singleSearchFixture(t)
			g.respond = func(_ context.Context, system, _ string) (string, error) {
				if strings.Contains(output, "matches") && strings.Contains(system, "alternate search") {
					return `{"queries":["drycleanres"]}`, nil
				}
				if !strings.Contains(output, "matches") && !strings.Contains(system, "alternate search") {
					return `{"matches":[{"ref":"S1","score":0.95}]}`, nil
				}
				return output, nil
			}
			page, err := c.SearchMemoryPageContext(context.Background(), "laundry")
			if err != nil || len(page.Hits) != 0 || !page.CoverageLimited {
				t.Fatal("malformed output escaped", page, err)
			}
		})
	}
}

func TestSingleModelRecallHonorsDomainScopeAndCloudConsent(t *testing.T) {
	c, _, id, g := singleSearchFixture(t)
	page, err := c.hybridPage(context.Background(), "laundry", search.ExactPage{}, semantic.Query{Domains: []string{"different.test"}}, 8)
	if err != nil || len(page.Hits) != 0 || g.calls != 1 {
		t.Fatal(page, g.calls, err)
	}
	c.localAI.settings.Provider, c.localAI.settings.RemoteAskEnabled = ProviderClaude, true
	page, err = c.hybridPage(context.Background(), "laundry", search.ExactPage{}, semantic.Query{Domains: []string{"example.test"}}, 8)
	if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != id || g.calls != 3 {
		t.Fatal("consented cloud recall unavailable", page, g.calls, err)
	}
}

func TestSingleModelRecallPolicyChangeCancelsLateResult(t *testing.T) {
	c, _, _, g := singleSearchFixture(t)
	c.localAI.path = filepath.Join(t.TempDir(), "ai.json")
	started, release := make(chan context.Context, 1), make(chan struct{})
	g.respond = func(ctx context.Context, _, _ string) (string, error) {
		started <- ctx
		<-release
		return `{"queries":["drycleanres"]}`, nil
	}
	done := make(chan error, 1)
	go func() { _, err := c.SearchMemoryContext(context.Background(), "laundry"); done <- err }()
	select {
	case inference := <-started:
		if err := c.ConfigureLocalAI(context.Background(), LocalAISettings{}); err != nil {
			close(release)
			t.Fatal(err)
		}
		if inference.Err() == nil {
			close(release)
			t.Fatal("policy failed to cancel model context")
		}
		close(release)
	case <-time.After(time.Second):
		close(release)
		t.Fatal("recall did not consult selected model")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("retired provider result escaped", err)
	}
}

func TestSingleModelTaskRecallUsesBoundedLiveTitleAndCurrentRevision(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	created, err := c.CreateTask(context.Background(), "Drycleanres on monday", nil)
	if err != nil {
		t.Fatal(err)
	}
	g := &singleSearchModel{}
	c.localAI = &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderApple}, askModel: g, service: notes.NewService(nil, nil, nil)}
	page, err := c.searchTasks(context.Background(), "pick up my clothes", []string{"clothes"}, semantic.Query{}, 8, true)
	if err != nil || len(page.matches) != 1 || page.matches[0].item.ID != created.Item.ID || g.calls != 1 {
		t.Fatal("selected model task recall missing", page, g.calls, err)
	}
	if !strings.Contains(g.inputs[0], "Drycleanres on monday") || strings.Contains(g.inputs[0], created.Item.ID) {
		t.Fatal("title ranking payload invalid", g.inputs)
	}
	exact, err := c.searchTasks(context.Background(), "drycleanres", []string{"drycleanres"}, semantic.Query{}, 8, true)
	if err != nil || len(exact.matches) != 1 || g.calls != 1 {
		t.Fatal("exact task lookup generated", exact, g.calls, err)
	}
	g.respond = func(_ context.Context, _, _ string) (string, error) {
		item := created.Item
		item.Title = "An unrelated current title"
		item.State = tasks.Completed
		if _, err := c.UpdateTask(context.Background(), item); err != nil {
			return "", err
		}
		return `{"matches":[{"ref":"S1","score":0.95}]}`, nil
	}
	page, err = c.searchTasks(context.Background(), "pick up my clothes", []string{"clothes"}, semantic.Query{}, 8, true)
	if err != nil || len(page.matches) != 0 {
		t.Fatal("stale task title published", page, err)
	}
}

func TestSingleModelBootstrapLeavesLegacyEmbeddingSettingsUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory-index.json")
	saved := []byte(`{"enabled":true,"endpoint":"http://127.0.0.1:11434","model":"embeddinggemma","dimensions":768}`)
	if err := os.WriteFile(path, saved, 0600); err != nil {
		t.Fatal(err)
	}
	app, err := openLocalAppAt(context.Background(), dir, &bootstrapSecrets{values: make(map[string][]byte)}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	if app.Client.MemoryIndexSettings().Enabled || app.Client.memoryIndex != nil {
		t.Fatal("standard bootstrap still starts hidden embedding model")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(saved) {
		t.Fatal("legacy settings were overwritten", err)
	}
}

func TestSingleModelExactSourceDoesNotGenerateForUnrelatedTasks(t *testing.T) {
	c, s, at := todayTaskClient(t)
	o, err := capture.SanitizeObservation("reader.app", "example.test", "Drycleanres on Monday", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(context.Background(), o, at); err != nil {
		t.Fatal(err)
	}
	g := &singleSearchModel{}
	c.localAI = &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderApple}, askModel: g, service: notes.NewService(s, nil, nil)}
	if _, err := c.CreateTask(context.Background(), "Review compiler benchmarks", nil); err != nil {
		t.Fatal(err)
	}
	page, err := c.SearchMemoryPageContext(context.Background(), "drycleanres")
	if err != nil || len(page.Hits) != 1 || g.calls != 0 {
		t.Fatal("exact lookup generated for unrelated tasks", page, g.calls, err)
	}
}

func TestSingleModelPolicyChangeCannotMissRecallRegistration(t *testing.T) {
	c, _, _, g := singleSearchFixture(t)
	c.localAI.path = filepath.Join(t.TempDir(), "ai.json")
	epoch, _ := c.BeginMemoryRead()
	c.memoryChangeMu.Lock()
	held := true
	defer func() {
		if held {
			c.memoryChangeMu.Unlock()
		}
	}()
	ready := make(chan context.Context, 1)
	done := make(chan struct{})
	go func() {
		_, _, ctx, finish, err := c.beginSelectedRecall(context.Background(), epoch)
		if err == nil {
			ready <- ctx
			<-done
			finish()
		} else {
			ready <- nil
		}
	}()
	deadline := time.Now().Add(time.Second)
	for c.aiPolicyMu.TryLock() {
		c.aiPolicyMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("policy snapshot was not protected")
		}
		runtime.Gosched()
	}
	configured := make(chan error, 1)
	go func() { configured <- c.ConfigureLocalAI(context.Background(), LocalAISettings{}) }()
	select {
	case err := <-configured:
		t.Fatal("configuration bypassed recall registration", err)
	case <-time.After(20 * time.Millisecond):
	}
	c.memoryChangeMu.Unlock()
	held = false
	ctx := <-ready
	if err := <-configured; err != nil {
		close(done)
		t.Fatal(err)
	}
	if ctx == nil || ctx.Err() == nil || g.calls != 0 {
		close(done)
		t.Fatal("retired recall remained active", ctx)
	}
	close(done)
}

func TestSingleModelRecallDoesNotPublishAfterDeletionDuringRanking(t *testing.T) {
	c, _, _, g := singleSearchFixture(t)
	g.respond = func(_ context.Context, system, _ string) (string, error) {
		if strings.Contains(system, "alternate search") {
			return `{"queries":["drycleanres"]}`, nil
		}
		c.NotifyMemoryChanging()
		c.NotifyMemoryStable()
		return `{"matches":[{"ref":"S1","score":0.95}]}`, nil
	}
	page, err := c.SearchMemoryPageContext(context.Background(), "laundry")
	if err == nil || len(page.Hits) != 0 {
		t.Fatal("ranked source escaped deletion fence", page, err)
	}
}

type singleSearchCounter struct {
	*singleSearchModel
	tokens int
}

func (g *singleSearchCounter) CountInputTokens(context.Context, string, string) (int, error) {
	return g.tokens, nil
}

func TestSingleModelRecallRejectsOversizedInputAndOutput(t *testing.T) {
	for _, mode := range []string{"tokens", "output", "query-count", "query-bytes", "duplicate-key"} {
		t.Run(mode, func(t *testing.T) {
			c, _, _, g := singleSearchFixture(t)
			switch mode {
			case "tokens":
				c.localAI.askModel = &singleSearchCounter{g, 8192}
			case "output":
				g.respond = func(context.Context, string, string) (string, error) { return strings.Repeat("x", 9000), nil }
			case "query-count":
				g.respond = func(context.Context, string, string) (string, error) { return `{"queries":["a","b","c","d","e"]}`, nil }
			case "query-bytes":
				g.respond = func(context.Context, string, string) (string, error) {
					return `{"queries":["` + strings.Repeat("a", 257) + `"]}`, nil
				}
			case "duplicate-key":
				g.respond = func(context.Context, string, string) (string, error) {
					return `{"queries":["wrong"],"queries":["drycleanres"]}`, nil
				}
			}
			page, err := c.SearchMemoryPageContext(context.Background(), "laundry")
			if err != nil || len(page.Hits) != 0 || !page.CoverageLimited {
				t.Fatal("unbounded recall escaped", page, err)
			}
			if mode == "tokens" && g.calls != 0 {
				t.Fatal("generation preceded token admission")
			}
		})
	}
}

func TestSingleModelScopedMissDoesNotReturnUnrankedRecentEvidence(t *testing.T) {
	c, _, _, g := singleSearchFixture(t)
	g.respond = func(_ context.Context, system, _ string) (string, error) {
		if strings.Contains(system, "alternate search") {
			return `{"queries":["drycleanres"]}`, nil
		}
		return `{"matches":[]}`, nil
	}
	plan := assistant.Plan{Kind: assistant.KindLookup, Question: "compiler", Terms: []string{"compiler"}, AppIDs: []string{"reader.app"}}
	evidence, _, err := c.lookupEvidence(context.Background(), plan, false, nil, nil)
	if err != nil || len(evidence) != 0 {
		t.Fatal("unranked unrelated source escaped scoped recall", evidence, err)
	}
}

func TestSingleModelAskDisclosesBoundedRecallCoverage(t *testing.T) {
	c, _, _, _ := singleSearchFixture(t)
	plan := assistant.Plan{Kind: assistant.KindLookup, Question: "laundry", Terms: []string{"laundry"}, MatchOnly: true}
	result, err := c.RetrievePlan(context.Background(), plan)
	if err != nil || len(result.Evidence) != 1 || result.Diagnosis.SemanticIssue != "limited" {
		t.Fatal("bounded recall coverage was lost", result, err)
	}
}

type singleSearchHydrationPolicy struct {
	*durable.Store
	reads  int
	change func()
}

func (d *singleSearchHydrationPolicy) GetSourceEvidence(ctx context.Context, id string) (capture.Evidence, bool, error) {
	d.reads++
	if d.reads == 2 {
		d.change()
	}
	return d.Store.GetSourceEvidence(ctx, id)
}
func TestSingleModelSearchCannotPublishAfterPolicyChangeDuringFinalHydration(t *testing.T) {
	c, s, _, g := singleSearchFixture(t)
	c.localAI.path = filepath.Join(t.TempDir(), "ai.json")
	changed := false
	spy := &singleSearchHydrationPolicy{Store: s, change: func() {
		g.mu.Lock()
		calls := g.calls
		g.mu.Unlock()
		if calls != 2 {
			t.Error("policy changed before rewrite and ranking finished", calls)
		}
		changed = true
		if err := c.ConfigureLocalAI(context.Background(), LocalAISettings{}); err != nil {
			t.Error(err)
		}
	}}
	c.data = spy
	page, err := c.SearchMemoryPageContext(context.Background(), "laundry")
	if !changed || spy.reads != 2 {
		t.Fatal("fixture missed the generic final hydration boundary", changed, spy.reads)
	}
	if !errors.Is(err, context.Canceled) || len(page.Hits) != 0 {
		t.Fatal("retired policy result escaped final hydration", page, err)
	}
}

type singleSearchObservedModel struct {
	configuredAskModel
	generator assistant.PreparedGeneralGenerator
	t         *testing.T
}

func (g *singleSearchObservedModel) GenerateGeneralPrompt(ctx context.Context, system, user string, max int) (string, error) {
	output, err := g.generator.GenerateGeneralPrompt(ctx, system, user, max)
	g.t.Logf("synthetic recall output: %s err=%v", output, err)
	return output, err
}
func (g *singleSearchObservedModel) GenerateGeneralJSONPrompt(ctx context.Context, system, user string, schema json.RawMessage, max int) (string, error) {
	output, err := g.configuredAskModel.(assistant.PreparedJSONGenerator).GenerateGeneralJSONPrompt(ctx, system, user, schema, max)
	g.t.Logf("synthetic structured recall output: %s err=%v", output, err)
	return output, err
}

func TestSingleModelRealLocalCompletionRecall(t *testing.T) {
	if os.Getenv("BLUR_AI_RECALL_INTEGRATION") != "1" {
		t.Skip("requires a discovered local completion model")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	catalog, err := model.DiscoverInstalledModels(ctx, "http://127.0.0.1:11434")
	if err != nil {
		t.Fatal(err)
	}
	name := ""
	for _, candidate := range catalog {
		if candidate.Completion {
			name = candidate.Name
			break
		}
	}
	if name == "" {
		t.Fatal("no completion-capable local model installed")
	}
	c, s, id, _ := singleSearchFixture(t)
	for _, text := range []string{"Review compiler benchmarks on Monday", "Book a dentist appointment", "Pay the mortgage"} {
		o, err := capture.SanitizeObservation("reader.app", "example.test", text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, o, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	g, err := assistant.NewOllamaGenerator("http://127.0.0.1:11434", name)
	if err != nil {
		t.Fatal(err)
	}
	defer g.CloseIdleConnections()
	c.localAI = &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderOllama, Model: name, Endpoint: "http://127.0.0.1:11434"}, askModel: &singleSearchObservedModel{g, g, t}, service: notes.NewService(s, nil, nil)}
	for _, query := range []string{"laundry", "pick up my clothes", "collect my coat", "drycleaners"} {
		page, err := c.SearchMemoryPageContext(ctx, query)
		if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != id {
			t.Fatalf("model=%s query=%q hits=%+v err=%v", name, query, page, err)
		}
		t.Logf("model=%s query=%q found synthetic misspelled drycleaning source", name, query)
	}
	taskClient, _, _ := todayTaskClient(t)
	taskClient.localAI = &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderOllama, Model: name}, askModel: &singleSearchObservedModel{g, g, t}, service: notes.NewService(nil, nil, nil)}
	created, err := taskClient.CreateTask(ctx, "Drycleanres on monday", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := taskClient.CreateTask(ctx, "Review compiler benchmarks", nil); err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{"laundry", "pick up my clothes"} {
		page, err := taskClient.searchTasks(ctx, question, []string{"clothes"}, semantic.Query{}, 8, true)
		if err != nil || len(page.matches) != 1 || page.matches[0].item.ID != created.Item.ID {
			t.Fatal("real selected-model task recall failed", question, page, err)
		}
		t.Logf("model=%s question=%q found synthetic misspelled current task", name, question)
	}
}

type singleSearchQueryAudit struct {
	*durable.Store
	queries []search.ExactQuery
}

func (d *singleSearchQueryAudit) SearchExact(ctx context.Context, q search.ExactQuery) (search.ExactPage, error) {
	d.queries = append(d.queries, q)
	return d.Store.SearchExact(ctx, q)
}
func TestSingleModelReformulationsKeepSightingScopeAndExclusions(t *testing.T) {
	c, s, id, _ := singleSearchFixture(t)
	d := &singleSearchQueryAudit{Store: s}
	c.data = d
	start, end := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	page, err := c.hybridPage(context.Background(), "laundry", search.ExactPage{}, semantic.Query{AppIDs: []string{"reader.app"}, Domains: []string{"example.test"}, Start: start, End: end, ScopeSourceIDs: []string{id}}, 8)
	if err != nil || len(page.Hits) != 1 || len(d.queries) != 1 {
		t.Fatal(page, d.queries, err)
	}
	q := d.queries[0]
	if q.AppID != "reader.app" || q.Domain != "example.test" || !q.CapturedStart.Equal(start) || !q.CapturedEnd.Equal(end) || len(q.ScopeSourceIDs) != 1 || q.ScopeSourceIDs[0] != id || len(q.ExcludeAppIDs) != 1 || q.ExcludeAppIDs[0] != "com.hazcod.blur" {
		t.Fatal("reformulation changed user scope", q)
	}
}

type singleSearchByteCounter struct{ *singleSearchModel }

func (g *singleSearchByteCounter) CountInputTokens(_ context.Context, system, user string) (int, error) {
	return len(system) + len(user), nil
}
func TestSingleModelRecallPacksMultiplePassagesWithinProviderBudget(t *testing.T) {
	c, s, _, g := singleSearchFixture(t)
	c.localAI.askModel = &singleSearchByteCounter{g}
	for i := 0; i < 15; i++ {
		text := strings.Repeat("long saved context ", 40) + " drycleanres " + strings.Repeat("more detail ", 40) + string(rune('A'+i))
		o, err := capture.SanitizeObservation("reader.app", "example.test", text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(context.Background(), o, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	page, err := c.SearchMemoryPageContext(context.Background(), "laundry")
	if err != nil || len(page.Hits) != 1 || g.calls != 2 {
		t.Fatal("candidate count exhausted provider budget", page, g.calls, err)
	}
	if len(g.inputs[1]) > 7000 {
		t.Fatal("ranking prompt exceeded input admission", len(g.inputs[1]))
	}
}

func TestSingleModelCoverageCopyDescribesRecallInQuickPanel(t *testing.T) {
	turn := quickConversationTurn{direct: true, coverageLimited: true, plan: assistant.Plan{Kind: assistant.KindLookup}}
	text := quickTurnMeta(turn)
	if !strings.Contains(text, "selected saved passages") || !strings.Contains(text, "may not be included") || strings.Contains(text, "recap") || strings.Contains(text, "without a model") {
		t.Fatal("recall was described as model-free recap", text)
	}
	generated := answerSentLine(false, false, 2, 1)
	if !strings.Contains(generated, "answer model") || strings.Contains(generated, "local model") {
		t.Fatal("cloud answer was described as local", generated)
	}
}

func TestSingleModelSearchRejectsNilClient(t *testing.T) {
	var c *Client
	if _, err := c.SearchMemoryPageContext(context.Background(), "laundry"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("nil search client was admitted", err)
	}
}
