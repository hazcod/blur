package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type groundedRetriever struct {
	planTestRetriever
	fresh      Retrieval
	freshCalls int
	freshPlan  Plan
	ids        []string
	cancel     context.CancelFunc
}

func (r *groundedRetriever) RetrieveSources(_ context.Context, p Plan, ids []string) (Retrieval, error) {
	r.freshCalls++
	r.freshPlan, r.ids = p, append([]string(nil), ids...)
	if r.cancel != nil {
		r.cancel()
	}
	return r.fresh, nil
}

func memoryFollowupHistory(id string) []Turn {
	return []Turn{{Question: "Who is Mira?", Answer: "Mira leads the Aurora project. PRIOR_ANSWER", SourceIDs: []string{id}, Plan: Plan{Kind: KindEntity, Entity: "Mira", Terms: []string{"mira"}, Question: "Who is Mira?", MatchOnly: true}}}
}

// These cases used to lose the subject, select today's recap, or omit the
// answer being transformed. All must use the cited, freshly read evidence.
func TestMemoryFollowupsUseFreshEvidenceAndPreviousAnswer(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, q := range []string{"Summarize that", "Make it shorter", "What does that mean?", "What is she working on?", "What is her project?", "What about her project?", "Who said that?"} {
		t.Run(q, func(t *testing.T) {
			r := &groundedRetriever{fresh: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Mira leads Aurora. FRESH_SOURCE"}}}}
			g := &testGenerator{answer: ModelAnswer{Text: "Mira leads Aurora.", SourceIDs: []string{id}}}
			got, err := NewService(r, g, testCounter{tokens: 10}, 8192).WithPersonalContext(true).AskWithHistory(context.Background(), q, memoryFollowupHistory(id))
			if err != nil || got.General || r.freshCalls != 1 || r.freshPlan.Entity != "Mira" || r.freshPlan.Question != "Who is Mira?" || len(r.ids) != 1 || r.ids[0] != id || got.ConversationTurns != 1 || g.calls != 1 || !strings.Contains(g.packed.User, "PRIOR_ANSWER") || !strings.Contains(g.packed.User, "FRESH_SOURCE") || strings.Contains(g.packed.User, "unrelated") {
				t.Fatalf("followup lost grounded reference: result=%+v fresh=%+v packed=%+v err=%v", got, r.freshPlan, g.packed, err)
			}
			if got.Plan.Kind == KindRecap || got.Plan.Entity != "Mira" || got.Plan.Question != q {
				t.Fatalf("followup changed subject: %+v", got.Plan)
			}
		})
	}
}

func TestShorteningDirectSavedQuoteUsesOnlyFreshSourceText(t *testing.T) {
	id := strings.Repeat("c", 64)
	quote := "The rollback owner is Mira."
	evidence := []Evidence{{SourceID: id, Text: quote}}
	history := []Turn{{Question: "What or who is Mira?", Answer: "Your saved source says: “" + quote + "”", Plan: Plan{Kind: KindEntity, Entity: "Mira", Terms: []string{"mira"}, Question: "What or who is Mira?"}, SourceIDs: []string{id}, SourceDigests: EvidenceDigests(evidence)}}
	r := &groundedRetriever{fresh: Retrieval{Evidence: evidence}}
	g := &testGenerator{}
	got, err := NewService(r, g, testCounter{tokens: 1}, 8192).WithPersonalContext(true).AskWithHistory(context.Background(), "Make it shorter", history)
	if err != nil || !got.Direct || got.General || got.Answer.Text != quote || len(got.Answer.SourceIDs) != 1 || got.Answer.SourceIDs[0] != id || len(got.Sources) != 1 || g.calls != 0 {
		t.Fatalf("verified direct quote was not shortened locally: result=%+v calls=%d err=%v", got, g.calls, err)
	}
}

func TestMemoryFollowupCannotReviveDeletedAnswer(t *testing.T) {
	id := strings.Repeat("a", 64)
	r := &groundedRetriever{}
	g := &testGenerator{}
	got, err := NewService(r, g, testCounter{tokens: 1}, 8192).WithPersonalContext(true).AskWithHistory(context.Background(), "Make it shorter", memoryFollowupHistory(id))
	if err != nil || !got.Answer.InsufficientEvidence || len(got.Sources) != 0 || g.calls != 0 || got.General || !strings.Contains(got.Answer.Text, "sources") {
		t.Fatalf("deleted source was treated as answer evidence: %+v calls=%d err=%v", got, g.calls, err)
	}
}

func TestMemoryFollowupDropsOldAnswerWhenOnlySomeSourcesSurvive(t *testing.T) {
	id, deleted := strings.Repeat("a", 64), strings.Repeat("b", 64)
	history := memoryFollowupHistory(id)
	history[0].SourceIDs = append(history[0].SourceIDs, deleted)
	r := &groundedRetriever{fresh: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Surviving current fact"}}}}
	g := &testGenerator{answer: ModelAnswer{Text: "Current fact.", SourceIDs: []string{id}}}
	got, err := NewService(r, g, testCounter{tokens: 1}, 8192).WithPersonalContext(true).AskWithHistory(context.Background(), "Summarize that", history)
	if err != nil || strings.Contains(g.packed.User, "PRIOR_ANSWER") || !strings.Contains(g.packed.User, "Surviving current fact") || got.ConversationTurns != 0 {
		t.Fatal("partly deleted prior answer leaked", got, g.packed, err)
	}
}

func TestMemoryFollowupDoesNotExpandRemoteConversationConsent(t *testing.T) {
	id := strings.Repeat("a", 64)
	r := &groundedRetriever{fresh: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Current fact"}}}}
	g := &testGenerator{answer: ModelAnswer{Text: "Current fact.", SourceIDs: []string{id}}}
	got, err := NewService(r, g, testCounter{tokens: 1}, 8192).AskWithHistory(context.Background(), "Make it shorter", memoryFollowupHistory(id))
	if err != nil || strings.Contains(g.packed.User, "PRIOR_ANSWER") || got.ConversationTurns != 0 {
		t.Fatal("old memory grant admitted conversation", got, g.packed, err)
	}
}

func TestMemoryFollowupPackingFitsSmallBudgetAndCurrentCorrectionsWin(t *testing.T) {
	id := strings.Repeat("a", 64)
	history := memoryFollowupHistory(id)
	history[0].Answer = strings.Repeat("Old answer. ", 800)
	r := &groundedRetriever{fresh: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Mira leads the old project", Origin: OriginInferred}, {SourceID: strings.Repeat("b", 64), Text: "Mira now leads Aurora", Origin: OriginUserCorrection}}}}
	g := &testGenerator{answer: ModelAnswer{Text: "Aurora.", SourceIDs: []string{strings.Repeat("b", 64)}}}
	got, err := NewService(r, g, byteCounter{}, 4000).WithPersonalContext(true).AskWithHistory(context.Background(), "Make it shorter", history)
	var payload map[string]json.RawMessage
	if err != nil || json.Unmarshal([]byte(g.packed.User), &payload) != nil || got.InputTokens+MaxAnswerTokens+PromptTemplateReserve > 4000 || !strings.Contains(g.packed.User, "Mira now leads Aurora") || got.ConversationTruncated == 0 {
		t.Fatal("bounded followup lost fresh correction", got, g.packed, err)
	}
}

func TestMemoryFollowupHonorsCancellationAfterFreshRead(t *testing.T) {
	id := strings.Repeat("a", 64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &groundedRetriever{fresh: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Private"}}}, cancel: cancel}
	g := &testGenerator{}
	got, err := NewService(r, g, testCounter{tokens: 1}, 8192).WithPersonalContext(true).AskWithHistory(ctx, "Make it shorter", memoryFollowupHistory(id))
	if !errors.Is(err, context.Canceled) || g.calls != 0 || len(got.Sources) != 0 {
		t.Fatal(got, err)
	}
}

func TestTimeExplanationRevalidatesStructuredEvidenceInsteadOfRepeating(t *testing.T) {
	r := &groundedRetriever{planTestRetriever: planTestRetriever{direct: &Answer{Text: "2 h in Slack."}}, fresh: Retrieval{Evidence: []Evidence{{SourceID: strings.Repeat("c", 64), Text: "Current foreground activity: 2 hours in Slack; this measures captured activity, not attention."}}}}
	g := &testGenerator{answer: ModelAnswer{Text: "It measures captured foreground activity.", SourceIDs: []string{strings.Repeat("c", 64)}}}
	prev, _ := PlanQuestion("How much time did I spend in Slack today?", time.Now(), nil)
	got, err := NewService(r, g, testCounter{tokens: 1}, 8192).WithPersonalContext(true).AskWithHistory(context.Background(), "What does that mean?", []Turn{{Question: prev.Question, Answer: "2 h in Slack.", Plan: prev}})
	if err != nil || got.Direct || g.calls != 1 || r.freshCalls != 1 || got.Answer.Text == "2 h in Slack." {
		t.Fatal(got, err)
	}
}

func TestMemoryContinuationDoesNotSendAuthoredContextUnderOldRemoteGrant(t *testing.T) {
	for _, origin := range []EvidenceOrigin{OriginUserFact, OriginTask} {
		t.Run(string(origin), func(t *testing.T) {
			id := strings.Repeat("a", 64)
			r := &groundedRetriever{fresh: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "AUTHORED_PRIVATE_SENTINEL", Origin: origin}}}}
			g := &testGenerator{answer: ModelAnswer{Text: "An authored fact.", SourceIDs: []string{id}}}
			got, err := NewService(r, g, testCounter{tokens: 1}, 8192).AskWithHistory(context.Background(), "Make it shorter", memoryFollowupHistory(id))
			if err != nil || g.calls != 0 || len(got.Sources) != 0 || !got.Answer.InsufficientEvidence {
				t.Fatal("old captured-memory grant admitted new authored context", got, g.packed, err)
			}
		})
	}
}

func TestGroundedFollowupDistinguishesNewRequestsAndAmbiguousPeople(t *testing.T) {
	for _, q := range []string{"Explain that compiler error", "Summarize that report about SQLite", "Write her a poem", "What is SQLite?", "Explain this: private text", "Make it shorter: current pasted text"} {
		if IsGroundedFollowUp(q) {
			t.Errorf("new request captured as previous-answer continuation: %q", q)
		}
	}
	id := strings.Repeat("a", 64)
	history := memoryFollowupHistory(id)
	history[0].Plan.Entity = "Mira and Ada"
	r := &groundedRetriever{fresh: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Mira and Ada."}}}}
	g := &testGenerator{}
	got, err := NewService(r, g, testCounter{tokens: 1}, 8192).WithPersonalContext(true).AskWithHistory(context.Background(), "What is her project?", history)
	if err != nil || !got.Answer.InsufficientEvidence || r.freshCalls != 0 || g.calls != 0 || !strings.Contains(got.Answer.Text, "referring") {
		t.Fatal("ambiguous pronoun guessed a person", got, err)
	}
}

func TestMemoryContinuationKeepsOriginalSubjectAcrossMultipleRewrites(t *testing.T) {
	id := strings.Repeat("a", 64)
	r := &groundedRetriever{fresh: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Mira leads Aurora."}}}}
	g := &testGenerator{answer: ModelAnswer{Text: "Aurora.", SourceIDs: []string{id}}}
	s := NewService(r, g, testCounter{tokens: 1}, 8192).WithPersonalContext(true)
	first, err := s.AskWithHistory(context.Background(), "Make it shorter", memoryFollowupHistory(id))
	if err != nil {
		t.Fatal(err)
	}
	history := []Turn{{Question: "Make it shorter", Answer: first.Answer.Text, SourceIDs: first.Answer.SourceIDs, Plan: first.Plan}}
	got, err := s.AskWithHistory(context.Background(), "What is her project?", history)
	if err != nil || r.freshPlan.Question != "Who is Mira?" || r.freshPlan.Entity != "Mira" || got.ConversationTurns != 1 {
		t.Fatal("repeated followup lost original retrieval subject", got, r.freshPlan, err)
	}
}

func TestMemoryContinuationDropsPriorProseWhenStableSourceContentChanged(t *testing.T) {
	id := strings.Repeat("a", 64)
	history := memoryFollowupHistory(id)
	history[0].SourceDigests = EvidenceDigests([]Evidence{{SourceID: id, Text: "Mira leads OldProject.", Origin: OriginInferred}})
	r := &groundedRetriever{fresh: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Mira leads Aurora.", Origin: OriginInferred}}}}
	g := &testGenerator{answer: ModelAnswer{Text: "Aurora.", SourceIDs: []string{id}}}
	got, err := NewService(r, g, testCounter{tokens: 1}, 8192).WithPersonalContext(true).AskWithHistory(context.Background(), "Summarize that", history)
	if err != nil || got.ConversationTurns != 0 || strings.Contains(g.packed.User, "PRIOR_ANSWER") || !strings.Contains(g.packed.User, "Mira leads Aurora.") {
		t.Fatal("stable handle revived replaced fact from old prose", got, g.packed, err)
	}
}

func TestGroundedFollowupAcceptsPoliteReadOnlyReferences(t *testing.T) {
	for _, q := range []string{"Can you make it shorter?", "Could you explain that in simpler terms?", "And what is her project?", "Please summarize this", "Make it a bit shorter"} {
		if !IsGroundedFollowUp(q) {
			t.Errorf("natural answer reference missed: %q", q)
		}
	}
	for _, q := range []string{"Can you explain quantum mechanics?", "And write a poem about Mira", "Make it shorter\nHere is new text"} {
		if IsGroundedFollowUp(q) {
			t.Errorf("independent request captured: %q", q)
		}
	}
}

func TestGroundedFollowupKeepsExplicitScopeAndAppOverrides(t *testing.T) {
	id := strings.Repeat("a", 64)
	r := &groundedRetriever{fresh: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Mira in Slack."}}}}
	g := &testGenerator{answer: ModelAnswer{Text: "Mira.", SourceIDs: []string{id}}}
	got, err := NewService(r, g, testCounter{tokens: 1}, 8192).WithPersonalContext(true).AskWithHistory(context.Background(), "What was she working on in Slack yesterday?", memoryFollowupHistory(id))
	if err != nil || r.freshPlan.Entity != "Mira" || len(r.freshPlan.AppIDs) != 1 || r.freshPlan.AppIDs[0] != "com.tinyspeck.slackmacgap" || r.freshPlan.Scope.Label != "yesterday" || !r.freshPlan.Scope.Explicit {
		t.Fatal("explicit followup filters were lost", got, r.freshPlan, err)
	}
}

func TestGroundedTransformGrammarAcceptsStylesWithoutCapturingNewActions(t *testing.T) {
	for _, q := range []string{"Make it more formal", "Make it friendlier", "Could you make your answer less technical?", "Make that concise and professional", "Make it a little more reassuring"} {
		if !IsGroundedFollowUp(q) {
			t.Errorf("answer transformation missed: %q", q)
		}
	}
	for _, q := range []string{"note: Make it more formal", "Create a task to make it friendlier", "Make it a reminder", "Make it shorter and save a note", "Make it happen", "Make it shorter. Here is a new paragraph", "Make it more formal\nNew supplied text"} {
		if IsGroundedFollowUp(q) {
			t.Errorf("new content/action captured as answer transform: %q", q)
		}
	}
}

func TestFactualFollowupPreservesExplicitDomainWithDots(t *testing.T) {
	id := strings.Repeat("a", 64)
	r := &groundedRetriever{fresh: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Mira reviewed a GitHub change."}}}}
	g := &testGenerator{answer: ModelAnswer{Text: "A GitHub change.", SourceIDs: []string{id}}}
	got, err := NewService(r, g, testCounter{tokens: 1}, 8192).WithPersonalContext(true).AskWithHistory(context.Background(), "What was she working on github.com yesterday?", memoryFollowupHistory(id))
	if err != nil || !got.Plan.SubjectFollowup || r.freshPlan.Entity != "Mira" || len(r.freshPlan.Domains) != 1 || r.freshPlan.Domains[0] != "github.com" || r.freshPlan.Scope.Label != "yesterday" {
		t.Fatal("domain punctuation lost factual reference", got, r.freshPlan, err)
	}
}

func capturedEvidence(id, app string, at time.Time) Evidence {
	return Evidence{SourceID: id, AppID: "com.tinyspeck.slackmacgap", AppName: app, Text: "Dima: the retest is on Thursday", CapturedAt: at}
}

type clockedGroundedRetriever struct {
	*groundedRetriever
	now time.Time
}

func (r clockedGroundedRetriever) AskTime() time.Time { return r.now }

func citedOnlyHistory(id string) []Turn {
	return []Turn{{Question: "what did the retest say", Answer: "Dima mentioned a retest.", SourceIDs: []string{id}, Plan: Plan{Kind: KindLookup, Terms: []string{"retest"}, Question: "what did the retest say"}}}
}

// The previous answer named no subject (its plan had no Entity), so a pronoun
// follow-up used to stop at "Who are you referring to?". It now re-reads the
// sources that answer cited.
func TestPronounFollowupResolvesThroughCitedSourcesWhenThePlanHadNoEntity(t *testing.T) {
	id := strings.Repeat("d", 64)
	for _, q := range []string{"what else did she say", "when did he say that", "what did they say"} {
		t.Run(q, func(t *testing.T) {
			r := &groundedRetriever{fresh: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Dima: the retest is on Thursday. FRESH_SOURCE"}}}}
			g := &testGenerator{answer: ModelAnswer{Text: "Dima mentioned it.", SourceIDs: []string{id}}}
			got, err := NewService(r, g, testCounter{tokens: 10}, 8192).WithPersonalContext(true).AskWithHistory(context.Background(), q, citedOnlyHistory(id))
			if err != nil || strings.Contains(got.Answer.Text, "Who are you referring to") || r.freshCalls != 1 || len(r.ids) != 1 || r.ids[0] != id {
				t.Fatalf("pronoun follow-up did not use cited sources: result=%+v fresh=%d ids=%v err=%v", got, r.freshCalls, r.ids, err)
			}
			if r.freshPlan.SubjectFollowup {
				t.Fatalf("a subject-less follow-up must read the cited sources, not run a subject lookup: %+v", r.freshPlan)
			}
		})
	}
}

func TestPronounFollowupWithNothingCitedStillAsksWhoIsMeant(t *testing.T) {
	history := citedOnlyHistory("")
	history[0].SourceIDs = nil
	r := &groundedRetriever{}
	got, err := NewService(r, &testGenerator{}, testCounter{tokens: 10}, 8192).AskWithHistory(context.Background(), "when did he say that", history)
	if err != nil || !strings.Contains(got.Answer.Text, "Who are you referring to") || r.freshCalls != 0 {
		t.Fatalf("an unanchored pronoun must still ask who is meant: %+v calls=%d err=%v", got, r.freshCalls, err)
	}
}

func TestWhenFollowupWithoutAGeneratorAnswersCaptureTimesAndCitesThem(t *testing.T) {
	loc := brussels(t)
	id, other := strings.Repeat("d", 64), strings.Repeat("e", 64)
	at := time.Date(2026, 11, 15, 13, 47, 0, 0, time.UTC) // 14:47 in Brussels
	g := &groundedRetriever{fresh: Retrieval{Evidence: []Evidence{capturedEvidence(id, "Slack", at), capturedEvidence(other, "Slack", at.Add(-48*time.Hour))}}}
	r := clockedGroundedRetriever{groundedRetriever: g, now: time.Date(2026, 11, 16, 15, 30, 0, 0, loc)}
	got, err := NewService(r, nil, nil, 8192).AskWithHistory(context.Background(), "when did he say that?", citedOnlyHistory(id))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Direct || got.General || got.Answer.InsufficientEvidence || !strings.Contains(got.Answer.Text, "Captured Sunday 15 November at 14:47 in Slack") {
		t.Fatalf("capture time answer = %+v", got.Answer)
	}
	if len(got.Answer.SourceIDs) != 1 || got.Answer.SourceIDs[0] != id || len(got.Sources) != 1 {
		t.Fatalf("the cited source must be the one asked about: %+v sources=%d", got.Answer, len(got.Sources))
	}
}

func TestWhenFollowupWithAGeneratorStillUsesTheModel(t *testing.T) {
	id := strings.Repeat("d", 64)
	r := &groundedRetriever{fresh: Retrieval{Evidence: []Evidence{capturedEvidence(id, "Slack", time.Date(2026, 11, 15, 13, 47, 0, 0, time.UTC))}}}
	g := &testGenerator{answer: ModelAnswer{Text: "On Sunday.", SourceIDs: []string{id}}}
	if _, err := NewService(r, g, testCounter{tokens: 10}, 8192).WithPersonalContext(true).AskWithHistory(context.Background(), "when did he say that", citedOnlyHistory(id)); err != nil || g.calls != 1 {
		t.Fatalf("model was skipped: calls=%d err=%v", g.calls, err)
	}
}

// Sources the earlier answer cited but the fresh read no longer returns are not
// replaced by other captures of the same person.
func TestWhenFollowupNeverFallsBackToUncitedSources(t *testing.T) {
	loc := brussels(t)
	cited, other := strings.Repeat("d", 64), strings.Repeat("e", 64)
	g := &groundedRetriever{fresh: Retrieval{Evidence: []Evidence{capturedEvidence(other, "Slack", time.Date(2026, 11, 15, 13, 47, 0, 0, time.UTC))}}}
	r := clockedGroundedRetriever{groundedRetriever: g, now: time.Date(2026, 11, 16, 15, 30, 0, 0, loc)}
	got, err := NewService(r, nil, nil, 8192).AskWithHistory(context.Background(), "when did he say that?", citedOnlyHistory(cited))
	if err != nil || !got.Answer.InsufficientEvidence || len(got.Answer.SourceIDs) != 0 || len(got.Sources) != 0 || !strings.Contains(got.Answer.Text, "which") {
		t.Fatalf("uncited sources were presented as the answer: %+v sources=%d err=%v", got.Answer, len(got.Sources), err)
	}
}
