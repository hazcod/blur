package client

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
)

func corpusLabels(c *scenarioCorpus, evidence []assistant.Evidence) []string {
	out := make([]string, 0, len(evidence))
	for _, item := range evidence {
		out = append(out, c.label(item.SourceID))
	}
	return out
}

func TestLookupRetrievalRanksTheRelaxedMessagingCaptureFirstAndFlagsRelaxedTerms(t *testing.T) {
	c := newScenarioCorpus(t)
	ctx := context.Background()
	plan, err := assistant.PlanQuestion("what did Dima say about the pentest retest?", c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.client.RetrievePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	labels := corpusLabels(c, retrieval.Evidence)
	if len(labels) == 0 || labels[0] != "slack-dima-retest" {
		t.Fatalf("order %v, want the Slack message that says \"retest\" first", labels)
	}
	if slices.Contains(labels, "okta-login") {
		t.Fatalf("a sign-in page that only shares two words must be demoted: %v", labels)
	}
	if !retrieval.Diagnosis.TermsRelaxed || !retrieval.Evidence[0].TermsRelaxed {
		t.Fatalf("a two-of-three-terms match must be flagged: diagnosis=%+v first=%+v", retrieval.Diagnosis, retrieval.Evidence[0])
	}
	// The Notion page with all three terms is still retrieved, but not flagged.
	for _, item := range retrieval.Evidence {
		if c.label(item.SourceID) == "notion-pentest-plan" && item.TermsRelaxed {
			t.Fatal("an all-terms match must not be flagged as relaxed")
		}
	}
}

func TestLookupRetrievalRequiresTheNamedPersonAndPrefersSpokenWordsForPromises(t *testing.T) {
	c := newScenarioCorpus(t)
	ctx := context.Background()
	plan, err := assistant.PlanQuestion("what did Youssef promise?", c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.client.RetrievePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	labels := corpusLabels(c, retrieval.Evidence)
	if len(labels) == 0 || labels[0] != "voice-youssef" {
		t.Fatalf("order %v, want the newest conversational capture naming Youssef first", labels)
	}
	if len(labels) > planEvidenceItems {
		t.Fatalf("evidence %d exceeds the packing limit %d", len(labels), planEvidenceItems)
	}
	// A capture that matches other terms but never names Youssef is excluded.
	unrelated := assistant.Plan{Kind: assistant.KindLookup, Terms: []string{"promise", "retest", "youssef"}, Entity: "Youssef", Question: "what did Youssef promise about the retest?"}
	got, err := c.client.RetrievePlan(ctx, unrelated)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range got.Evidence {
		if label := c.label(item.SourceID); label == "notion-pentest-plan" || label == "okta-login" {
			t.Fatalf("%s does not name the entity and must not be retrieved: %v", label, corpusLabels(c, got.Evidence))
		}
	}
}

func TestRecapOverAWeekIsChronologicalAndCoversEveryDayWithCaptures(t *testing.T) {
	c := newScenarioCorpus(t)
	ctx := context.Background()
	plan, err := assistant.PlanQuestion("what did I do last week?", c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.client.RetrievePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !retrieval.Diagnosis.Chronological {
		t.Fatal("a seven-day recap must ask for chronological packing")
	}
	var previous time.Time
	days := map[string]bool{}
	for _, item := range retrieval.Evidence {
		if item.Layer >= assistant.LayerDay {
			continue
		}
		if item.CapturedAt.Before(previous) {
			t.Fatalf("evidence is not oldest first: %v", corpusLabels(c, retrieval.Evidence))
		}
		previous = item.CapturedAt
		days[item.CapturedAt.In(c.loc).Format("2006-01-02")] = true
	}
	for _, label := range []string{"sublime-sync-1", "terminal-sync-2", "terminal-venice", "note-buildwise", "slack-samira-1", "slack-dima-1"} {
		day := c.times[label].In(c.loc).Format("2006-01-02")
		if c.times[label].Before(plan.Scope.Start) || !c.times[label].Before(plan.Scope.End) {
			continue // outside the window
		}
		if !days[day] {
			t.Errorf("%s's day %s has captures but is absent from %v", label, day, corpusLabels(c, retrieval.Evidence))
		}
	}
}

func TestRecapOfADayKeepsNewestFirstOrderAndFlagsNoTimeline(t *testing.T) {
	c := newScenarioCorpus(t)
	plan, err := assistant.PlanQuestion("what did I do today", c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.client.RetrievePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if retrieval.Diagnosis.Chronological {
		t.Fatal("a window of 24 hours or less must keep its established order")
	}
}

func TestWindowEvidenceDropsLoginChromeWhenSubstantiveCapturesExist(t *testing.T) {
	c := newScenarioCorpus(t)
	ctx := context.Background()
	zone := c.loc
	day := time.Date(2026, 11, 15, 0, 0, 0, 0, zone)
	plan := assistant.Plan{Kind: assistant.KindCapture, Scope: assistant.Scope{Start: day, End: day.AddDate(0, 0, 1), Label: "yesterday", Explicit: true}, Question: "what did I capture yesterday"}
	retrieval, err := c.client.RetrievePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	labels := corpusLabels(c, retrieval.Evidence)
	if slices.Contains(labels, "okta-login") || len(labels) == 0 {
		t.Fatalf("capture evidence %v must not list the sign-in page next to real work", labels)
	}
	direct, handled, err := c.client.AnswerDirect(ctx, plan)
	if err != nil || !handled {
		t.Fatalf("direct answer handled=%t err=%v", handled, err)
	}
	for _, id := range direct.SourceIDs {
		if c.label(id) == "okta-login" {
			t.Fatalf("the no-model answer cites the sign-in page: %v", direct.Text)
		}
	}
	// Windows that only hold the sign-in page still return it.
	login := time.Date(2026, 11, 15, 9, 0, 0, 0, zone)
	only := assistant.Plan{Kind: assistant.KindCapture, Scope: assistant.Scope{Start: login.Add(-30 * time.Minute), End: login.Add(30 * time.Minute), Label: "yesterday morning", Explicit: true}, Question: "what did I capture yesterday morning"}
	got, err := c.client.RetrievePlan(ctx, only)
	if err != nil {
		t.Fatal(err)
	}
	if labels := corpusLabels(c, got.Evidence); !slices.Contains(labels, "okta-login") {
		t.Fatalf("a window that holds only the sign-in page must keep it, got %v", labels)
	}
}
