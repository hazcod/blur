package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
)

// A recency question that finds nothing in its own hour widens once, says how
// far it looked, and still never returns Blur's own interface.
func TestJustNowWidensOnceAndRecordsIt(t *testing.T) {
	corpus := newScenarioCorpus(t)
	ctx := context.Background()
	later := corpus.now.Add(150 * time.Minute) // 18:00: the last hour is empty
	corpus.client.askNow = func() time.Time { return later }
	plan, err := assistant.PlanQuestion("what was I just working on", later, nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := corpus.client.RetrievePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if retrieval.Diagnosis.ScopeWidened != assistant.ScopeJustNowWiden {
		t.Fatalf("widening was not recorded: %+v", retrieval.Diagnosis)
	}
	var cited []string
	for _, item := range retrieval.Evidence {
		if item.AppID == appidentity.BundleID {
			t.Fatalf("Blur's own interface was returned: %+v", item)
		}
		cited = append(cited, corpus.label(item.SourceID))
	}
	if len(cited) != 1 || cited[0] != "notion-pentest-plan" {
		t.Fatalf("widened evidence = %v, want only notion-pentest-plan", cited)
	}
	direct, handled, err := corpus.client.AnswerDirect(ctx, plan)
	if err != nil || !handled || !strings.Contains(direct.Text, "nothing in the last hour") || len(direct.Sources) != 1 {
		t.Fatalf("direct answer must name the widened window: %+v handled=%t err=%v", direct, handled, err)
	}
}

func TestJustNowWithinTheHourDoesNotWiden(t *testing.T) {
	corpus := newScenarioCorpus(t)
	plan, err := assistant.PlanQuestion("what was I just working on", corpus.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := corpus.client.RetrievePlan(context.Background(), plan)
	if err != nil || retrieval.Diagnosis.ScopeWidened != 0 || len(retrieval.Evidence) != 1 {
		t.Fatalf("unexpected widening: %+v err=%v", retrieval, err)
	}
}

// A chat sidebar lists every contact, so another person's DM must not be
// returned for a conversation with Samira.
func TestConversationRecapReturnsOnlyThePersonsThreadOldestFirst(t *testing.T) {
	corpus := newScenarioCorpus(t)
	plan, err := assistant.PlanQuestion("recap my convo with Samira", corpus.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := corpus.client.RetrievePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, item := range retrieval.Evidence {
		got = append(got, corpus.label(item.SourceID))
	}
	want := []string{"slack-samira-1", "slack-samira-2", "slack-samira-3", "slack-samira-4"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("conversation evidence = %v, want %v oldest first", got, want)
	}
}
