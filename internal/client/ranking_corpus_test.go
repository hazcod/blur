package client

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
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
	if !retrieval.Evidence[0].TermsRelaxed {
		t.Fatalf("a two-of-three-terms match must be flagged: first=%+v", retrieval.Evidence[0])
	}
	// The diagnosis follows what was actually packed, so it is set by the answer.
	for _, mode := range []scenarioMode{modeNoModel, modeFakeGen} {
		result, err := c.ask(ctx, mode, "what did Dima say about the pentest retest?", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !result.TermsRelaxed {
			t.Fatalf("%s: a packed two-of-three-terms match must set Result.TermsRelaxed", mode)
		}
	}
	allTerms, err := c.ask(ctx, modeNoModel, "where is the API key for venice stored again?", nil)
	if err != nil || allTerms.TermsRelaxed {
		t.Fatalf("an all-terms answer must not be relaxed: relaxed=%t err=%v", allTerms.TermsRelaxed, err)
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

func TestExplicitLoginQuestionKeepsTheSignInPage(t *testing.T) {
	c := newScenarioCorpus(t)
	ctx := context.Background()
	day := time.Date(2026, 11, 15, 0, 0, 0, 0, c.loc)
	scope := assistant.Scope{Start: day, End: day.AddDate(0, 0, 1), Label: "yesterday", Explicit: true}
	for _, question := range []string{"what did I capture on the Okta sign-in page yesterday", "what did I capture on the login page yesterday"} {
		plan := assistant.Plan{Kind: assistant.KindCapture, Scope: scope, Question: question}
		retrieval, err := c.client.RetrievePlan(ctx, plan)
		if err != nil {
			t.Fatal(err)
		}
		if labels := corpusLabels(c, retrieval.Evidence); !slices.Contains(labels, "okta-login") {
			t.Fatalf("%q: an explicit sign-in question must keep the sign-in page, got %v", question, labels)
		}
	}
	// A recap names a window; asking about the login page keeps it as well.
	recap := assistant.Plan{Kind: assistant.KindRecap, Scope: scope, Question: "recap the okta sign-in page yesterday"}
	retrieval, err := c.client.RetrievePlan(ctx, recap)
	if err != nil {
		t.Fatal(err)
	}
	if labels := corpusLabels(c, retrieval.Evidence); !slices.Contains(labels, "okta-login") {
		t.Fatalf("recap about the sign-in page lost it: %v", labels)
	}
	// A lookup naming the sign-in page keeps a partial sign-in match.
	lookup, err := assistant.PlanQuestion("what did Dima say on the okta login pentest page?", c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.client.RetrievePlan(ctx, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if labels := corpusLabels(c, got.Evidence); !slices.Contains(labels, "okta-login") {
		t.Fatalf("a lookup that asks about the login page must keep partial sign-in matches: %v", labels)
	}
}

func TestCaptureEvidenceDemotesBeforeTheCutSoTheListIsNotThin(t *testing.T) {
	c := newScenarioCorpus(t)
	ctx := context.Background()
	// Yesterday's newest captures are sign-in screens. A cut to six before
	// demotion would leave a thin list; demoting first must still return six.
	day := time.Date(2026, 11, 15, 0, 0, 0, 0, c.loc)
	for i := 0; i < 4; i++ {
		observation, err := capture.SanitizeObservation("com.example.Browser", "login.example.com", fmt.Sprintf("Sign in to account %d\nPassword\nRemember me\nVerification code", i), capture.MaxObservationTextBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.store.PutObservationAt(ctx, observation, day.Add(20*time.Hour+time.Duration(i)*time.Minute).UTC()); err != nil {
			t.Fatal(err)
		}
	}
	plan := assistant.Plan{Kind: assistant.KindCapture, Scope: assistant.Scope{Start: day, End: day.AddDate(0, 0, 1), Label: "yesterday", Explicit: true}, Question: "what did I capture yesterday"}
	start, end, _, _ := c.client.planWindow(plan)
	segments, err := c.client.windowSegments(ctx, start, end, MaxActivityReportSegments)
	if err != nil {
		t.Fatal(err)
	}
	items, _, _, err := c.client.captureEvidence(ctx, plan, start, end, filterSegments(segments, plan), 6)
	if err != nil {
		t.Fatal(err)
	}
	labels := corpusLabels(c, items)
	if slices.Contains(labels, "okta-login") || len(labels) != 6 {
		t.Fatalf("got %v, want six substantive captures and no sign-in page", labels)
	}
	for _, item := range items {
		if looksLikeAuthenticationPage(item.Text) {
			t.Fatalf("sign-in screen %q survived demotion", item.Text)
		}
	}
}
