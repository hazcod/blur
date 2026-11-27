package client

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func multiTechnicalFixture(t *testing.T) (*Client, *durable.Store, []string, map[string]string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	var subjects []string
	wanted := map[string]string{}
	filler := strings.Repeat("Unrelated package verification passed. ", 120)
	for i := 1; i <= 10; i++ {
		subject := fmt.Sprintf("agent_build_%02d", i)
		subjects = append(subjects, subject)
		line := fmt.Sprintf("/root/%s finished verified change %02d.\n", subject, i)
		text := line + filler + filler
		if i%3 == 1 {
			text = filler + line + filler
		} else if i%3 == 2 {
			text = filler + filler + line
		}
		o, err := capture.SanitizeObservation("com.apple.terminal", "", text, 16384)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(context.Background(), o, now.Add(-3*time.Hour)); err != nil {
			t.Fatal(err)
		}
		wanted[subject] = o.ContentID()
		for _, decoy := range []struct {
			app, label string
			at         time.Time
		}{
			{"com.apple.terminal", "/root/" + subject + "_v2", now.Add(-time.Minute)},
			{"com.apple.terminal", "prefix_" + subject, now.Add(-time.Minute)},
			{"com.tinyspeck.slackmacgap", subject, now.Add(-time.Minute)},
			{"com.apple.terminal", subject, now.Add(-24 * time.Hour)},
		} {
			o, err := capture.SanitizeObservation(decoy.app, "", decoy.label+" finished WRONG unrelated rollout.", 4096)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.PutObservationAt(context.Background(), o, decoy.at); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(s)
	t.Cleanup(c.Close)
	c.askNow = func() time.Time { return now }
	return c, s, subjects, wanted
}

func TestAskFindsDifferentTechnicalSubjectsInDifferentEncryptedSources(t *testing.T) {
	for _, count := range []int{2, 10} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			c, _, subjects, wanted := multiTechnicalFixture(t)
			question := "What did " + strings.Join(subjects[:count], " and ") + " each finish in Terminal today?"
			got, err := c.AskMemory(context.Background(), question)
			if err != nil || got.Answer.InsufficientEvidence || len(got.Sources) != count {
				t.Fatalf("separately saved agent work was discarded: subjects=%v sources=%d plan=%+v insufficient=%v err=%v", subjects[:count], len(got.Sources), got.Plan, got.Answer.InsufficientEvidence, err)
			}
			cited := map[string]bool{}
			for _, id := range got.Answer.SourceIDs {
				cited[id] = true
			}
			for i, subject := range subjects[:count] {
				if !cited[wanted[subject]] || !strings.Contains(got.Answer.Text, fmt.Sprintf("%s finished verified change %02d", subject, i+1)) {
					t.Fatalf("agent work lost independent grounding: subject=%s cited=%v answer=%q", subject, cited[wanted[subject]], got.Answer.Text)
				}
			}
			if strings.Contains(got.Answer.Text, "WRONG") {
				t.Fatal("comparison admitted a prefix, app, or date distractor", got.Answer.Text)
			}
		})
	}
}

func TestAskReportsMissingTechnicalSubjectWithoutDiscardingCoveredAgent(t *testing.T) {
	c, _, _, wanted := multiTechnicalFixture(t)
	got, err := c.AskMemory(context.Background(), "What did agent_build_01 and agent_build_11 each finish in Terminal today?")
	if err != nil || len(got.Sources) != 1 || got.Sources[0].SourceID != wanted["agent_build_01"] || !got.CoverageLimited || !strings.Contains(got.Answer.Text, "agent_build_11") || !strings.Contains(got.Answer.Text, "agent_build_01 finished verified change 01") {
		t.Fatalf("missing agent erased known work or its gap was hidden: %+v err=%v", got, err)
	}
}

func TestTechnicalComparisonKeepsSearchConjunctionSemantics(t *testing.T) {
	c, _, _, _ := multiTechnicalFixture(t)
	page, err := c.SearchContext(context.Background(), "agent_build_01 agent_build_02")
	if err != nil || len(page.Hits) != 0 {
		t.Fatalf("exact Search conjunction became a subject union: %+v err=%v", page, err)
	}
	got, err := c.AskMemory(context.Background(), "Does agent_build_01 call agent_build_02 in Terminal today?")
	if err != nil || len(got.Sources) != 0 || !got.Answer.InsufficientEvidence {
		t.Fatalf("relationship Ask became a subject union: sources=%d plan=%+v err=%v", len(got.Sources), got.Plan, err)
	}
	plan, err := assistant.PlanQuestion("What did agent_build_01 and agent_build_02 each finish in Terminal today?", c.AskTime(), nil)
	if err != nil || len(plan.TechnicalSubjects) != 2 {
		t.Fatalf("comparison fixture did not preserve both requested identifiers: %+v %v", plan, err)
	}
}

func TestTechnicalSubjectCandidatePathPreservesExtraConjunctiveTerms(t *testing.T) {
	c, _, _, _ := multiTechnicalFixture(t)
	plan, err := assistant.PlanQuestion("What did agent_build_01 finish in Terminal today?", c.AskTime(), nil)
	if err != nil {
		t.Fatal(err)
	}
	plan.Terms, err = search.NormalizeTokens("agent_build_01 canaryflag", search.MaxTerms)
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.planSearch(context.Background(), plan, true, nil)
	if err != nil || len(page.Hits) != 0 {
		t.Fatalf("identifier overfetch dropped a required attribute term: hits=%d err=%v", len(page.Hits), err)
	}
	plan.Question = "What did agent_build_01 and agent_build_02 each finish?"
	plan.TechnicalComparison = true
	plan.TechnicalSubjects = []string{"agent_build_01", "agent_build_02"}
	plan.Terms, _ = search.NormalizeTokens("agent_build_01 agent_build_02 canaryflag", search.MaxTerms)
	if technicalComparison(plan) {
		t.Fatal("comparison discarded an extra mandatory literal term")
	}
}

func TestTechnicalComparisonPreservesDeletionGate(t *testing.T) {
	c, store, _, wanted := multiTechnicalFixture(t)
	if err := store.DeleteObservationRecord(context.Background(), wanted["agent_build_01"]); err != nil {
		t.Fatal(err)
	}
	got, err := c.AskMemory(context.Background(), "What did agent_build_01 and agent_build_02 each finish in Terminal today?")
	if err != nil || len(got.Sources) != 1 || got.Sources[0].SourceID != wanted["agent_build_02"] || !got.CoverageLimited || !strings.Contains(got.Answer.Text, "couldn’t verify agent_build_01") {
		t.Fatalf("deleted agent work was resurrected or hid the surviving agent: sources=%d answer=%q err=%v", len(got.Sources), got.Answer.Text, err)
	}
}

func TestTechnicalComparisonPreservesCurrentNoteRevisionGate(t *testing.T) {
	c, store, _, _ := multiTechnicalFixture(t)
	ctx := context.Background()
	at := c.AskTime().Add(-time.Hour)
	old, err := store.CreateNoteDocument(ctx, "agent_notes_old finished the discarded prototype.", at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateNoteDocument(ctx, old.ID, old.Revision, "This note now describes a different topic.", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	live, err := store.CreateNoteDocument(ctx, "agent_notes_keep finished the verified parser.", at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.AskMemory(ctx, "What did agent_notes_old and agent_notes_keep each finish today?")
	if err != nil || len(got.Sources) != 1 || got.Sources[0].SourceID != live.SourceID || !strings.Contains(got.Answer.Text, "couldn’t verify agent_notes_old") || strings.Contains(got.Answer.Text, "discarded prototype") {
		t.Fatalf("historical note revision resurrected in comparison: sources=%d answer=%q err=%v", len(got.Sources), got.Answer.Text, err)
	}
}

func TestTechnicalComparisonKeepsKnownPassageWhenModelCannotCompareMissingAgent(t *testing.T) {
	c, store, _, wanted := multiTechnicalFixture(t)
	g := &singleSearchModel{answer: assistant.ModelAnswer{Text: "I cannot compare both agents.", InsufficientEvidence: true}}
	c.localAI = &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}, askModel: g, service: notes.NewService(store, nil, nil)}
	got, err := c.AskMemory(context.Background(), "What did agent_build_01 and agent_build_11 each finish in Terminal today?")
	if err != nil || len(got.Sources) != 1 || got.Sources[0].SourceID != wanted["agent_build_01"] || !strings.Contains(got.Answer.Text, "verified change 01") || !strings.Contains(got.Answer.Text, "couldn’t verify agent_build_11") {
		t.Fatalf("model uncertainty erased independently verified work: sources=%d answer=%q err=%v", len(got.Sources), got.Answer.Text, err)
	}
}

func TestTechnicalComparisonRejectsOneCitationForEveryAgentClaim(t *testing.T) {
	c, store, _, wanted := multiTechnicalFixture(t)
	g := &singleSearchModel{answer: assistant.ModelAnswer{Text: "agent_build_01 and agent_build_02 both finished every verified change.", SourceIDs: []string{wanted["agent_build_01"]}}}
	c.localAI = &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}, askModel: g, service: notes.NewService(store, nil, nil)}
	got, err := c.AskMemory(context.Background(), "What did agent_build_01 and agent_build_02 each finish in Terminal today?")
	if err != nil || strings.Contains(got.Answer.Text, "both finished every") || len(got.Answer.SourceIDs) != 2 || !strings.Contains(got.Answer.Text, "verified change 02") {
		t.Fatalf("one agent citation was treated as coverage for every agent: %+v err=%v", got, err)
	}
}

func TestTechnicalComparisonNamesSubjectsOmittedByRealContextBudget(t *testing.T) {
	c, _, subjects, wanted := multiTechnicalFixture(t)
	question := "What did " + strings.Join(subjects, " and ") + " each finish in Terminal today?"
	got, err := assistant.NewService(c, nil, nil, 4096).Ask(context.Background(), question)
	if err != nil || len(got.Sources) == 0 || len(got.Sources) >= 10 || !got.CoverageLimited {
		t.Fatalf("small actual context budget was not reported: sources=%d limited=%v err=%v", len(got.Sources), got.CoverageLimited, err)
	}
	cited := map[string]bool{}
	for _, id := range got.Answer.SourceIDs {
		cited[id] = true
	}
	for _, subject := range subjects {
		if !cited[wanted[subject]] && !strings.Contains(got.Answer.Text, subject) {
			t.Fatalf("context omitted an agent without naming its coverage gap: subject=%s answer=%q", subject, got.Answer.Text)
		}
	}
}

func technicalInteractionClient(t *testing.T, texts ...string) (*Client, *durable.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	for _, text := range texts {
		o, err := capture.SanitizeObservation("com.apple.terminal", "", text, 32768)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(context.Background(), o, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(s)
	c.askNow = func() time.Time { return now }
	t.Cleanup(c.Close)
	return c, s
}

func TestTechnicalComparisonKeepsDisjointPassagesInOneSource(t *testing.T) {
	text := "agent_build_01 finished zephyr calibration.\n" + strings.Repeat("Neutral package logs.\n", 400) + "agent_build_02 finished iris deployment.\n"
	c, _ := technicalInteractionClient(t, text)
	got, err := c.AskMemory(context.Background(), "What did agent_build_01 and agent_build_02 each finish in Terminal today?")
	if err != nil || len(got.Sources) != 1 || len(got.Answer.SourceIDs) != 1 || got.Sources[0].Text != text || len(got.Sources[0].MatchSpans) != 2 || !strings.Contains(got.Answer.Text, "zephyr calibration") || !strings.Contains(got.Answer.Text, "iris deployment") || !strings.Contains(got.Answer.Text, "\n…\n") || strings.Contains(got.Answer.Text, "couldn’t verify") {
		t.Fatalf("same-source subject passage was discarded or original changed: sources=%d answer=%q err=%v", len(got.Sources), got.Answer.Text, err)
	}
}

func TestTechnicalComparisonSharesContextBySubjectsAcrossMixedSources(t *testing.T) {
	var shared strings.Builder
	var subjects []string
	for i := 1; i <= 10; i++ {
		subject := fmt.Sprintf("agent_build_%02d", i)
		subjects = append(subjects, subject)
		if i < 10 {
			fmt.Fprintf(&shared, "%s completed verified change %02d.\n", subject, i)
			shared.WriteString(strings.Repeat("Neutral package logs.\n", 75))
		}
	}
	c, _ := technicalInteractionClient(t, shared.String(), "agent_build_10 completed verified change 10.")
	got, err := c.AskMemory(context.Background(), "What did "+strings.Join(subjects, " and ")+" each finish in Terminal today?")
	if err != nil || len(got.Sources) != 2 || len(got.Answer.SourceIDs) != 2 || strings.Contains(got.Answer.Text, "couldn’t verify") {
		t.Fatalf("mixed source distribution lost supported subjects: sources=%d answer=%q err=%v", len(got.Sources), got.Answer.Text, err)
	}
	for i, subject := range subjects {
		if !strings.Contains(got.Answer.Text, fmt.Sprintf("%s completed verified change %02d", subject, i+1)) {
			t.Fatalf("source row balancing discarded subject %s despite room in context", subject)
		}
	}
}

func TestTechnicalComparisonFollowupsKeepIndependentSubjectSet(t *testing.T) {
	c, store := technicalInteractionClient(t,
		"agent_build_01 is blocked by a pending parser fix.",
		"agent_build_02 completed iris deployment successfully.",
		"agent_build_99 is blocked by a completely unrelated deploy.")
	question := "What did agent_build_01 and agent_build_02 each finish in Terminal today?"
	first, err := c.AskMemory(context.Background(), question)
	if err != nil || len(first.Sources) != 2 {
		t.Fatalf("initial comparison: sources=%d err=%v", len(first.Sources), err)
	}
	history := quickHistoryTurns(appendQuickConversation(nil, question, first))
	got, err := c.AskMemoryWithHistory(context.Background(), "Which one is blocked?", history)
	if err != nil || len(got.Sources) != 2 || !got.Plan.TechnicalComparison || !strings.Contains(got.Answer.Text, "pending parser fix") || strings.Contains(got.Answer.Text, "agent_build_99") {
		t.Fatalf("comparison pronoun escaped its subject set: sources=%d answer=%q err=%v", len(got.Sources), got.Answer.Text, err)
	}
	for _, text := range []string{"agent_build_01 finished earlier parser calibration yesterday.", "agent_build_02 finished earlier iris rollout yesterday."} {
		o, err := capture.SanitizeObservation("com.apple.terminal", "", text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(context.Background(), o, c.AskTime().Add(-24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	got, err = c.AskMemoryWithHistory(context.Background(), "And yesterday?", history)
	if err != nil || len(got.Sources) != 2 || !strings.Contains(got.Answer.Text, "earlier parser calibration") || !strings.Contains(got.Answer.Text, "earlier iris rollout") {
		t.Fatalf("dated comparison became same-source AND: sources=%d answer=%q err=%v", len(got.Sources), got.Answer.Text, err)
	}
	for _, next := range []string{"Did agent_build_01 and agent_build_02 call each other in Terminal today?", "Do they call each other?"} {
		got, err = c.AskMemoryWithHistory(context.Background(), next, history)
		if err != nil || len(got.Sources) != 0 || !got.Answer.InsufficientEvidence || got.Plan.TechnicalComparison {
			t.Fatalf("relationship question inherited independent comparison: question=%q sources=%d plan=%+v err=%v", next, len(got.Sources), got.Plan, err)
		}
	}
	got, err = c.AskMemoryWithHistory(context.Background(), "And agent_build_02?", history)
	if err != nil || len(got.Sources) != 1 || got.Plan.TechnicalComparison || len(got.Plan.TechnicalSubjects) != 1 || got.Plan.TechnicalSubjects[0] != "agent_build_02" {
		t.Fatalf("explicit subject subset kept obsolete comparison subjects: sources=%d plan=%+v err=%v", len(got.Sources), got.Plan, err)
	}
}
