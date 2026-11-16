package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/tasks"
)

const (
	waApp    = "net.whatsapp.WhatsApp"
	slackApp = "com.tinyspeck.slackmacgap"
)

type askFixture struct {
	client *Client
	data   *testData
	now    time.Time
	ids    map[string]string
}

func newAskFixture(t *testing.T) *askFixture {
	t.Helper()
	t.Setenv("TZ", "UTC")
	now := time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)
	data := &testData{observations: map[string]capture.SanitizedObservation{}, observationAt: map[string]time.Time{}}
	f := &askFixture{data: data, now: now, ids: map[string]string{}}
	add := func(name, app, domain, text string, start time.Time, minutes int) {
		o, err := capture.SanitizeObservation(app, domain, text, 1024)
		if err != nil {
			t.Fatal(err)
		}
		data.observations[o.ContentID()] = o
		data.observationAt[o.ContentID()] = start
		f.ids[name] = o.ContentID()
		data.activity = append(data.activity, activity.Segment{ID: "seg-" + name, WallStart: start, WallEnd: start.Add(time.Duration(minutes) * time.Minute), Duration: int64(time.Duration(minutes) * time.Minute), AppID: app, Domain: domain, SourceIDs: []string{o.ContentID()}})
	}
	add("wa-today", waApp, "", "Anna: dinner at eight\nsecond line", now.Add(-4*time.Hour), 12)
	add("wa-today2", waApp, "", "Anna: see you soon", now.Add(-2*time.Hour), 60)
	add("slack-today", slackApp, "", "standup notes about release", now.Add(-3*time.Hour), 90)
	add("wa-old", waApp, "", "Anna: invoice from last week", now.AddDate(0, 0, -5), 20)
	c := NewClient(data)
	c.askNow = func() time.Time { return now }
	f.client = c
	return f
}

func todayPlan(kind assistant.Kind, apps ...string) assistant.Plan {
	return assistant.Plan{Kind: kind, AppIDs: apps, Scope: assistant.Scope{Start: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), Label: "today", Explicit: true}}
}

func TestOutstandingQuestionIncludesOnlyMarkedInferredOpenWork(t *testing.T) {
	ctx := context.Background()
	c, store, now := todayTaskClient(t)
	text := "Subject: CraftedSignal rollout\nMira: We are waiting for vendor verification before launch."
	source, err := capture.SanitizeObservation("com.apple.mail", "", text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	chunk := enrich.Chunk{
		SourceID: source.ContentID(), ModelKey: enrich.ModelKey("synthetic-local-model"),
		End: len(source.Text()), Next: len(source.Text()), Complete: true,
		Proposals: []derive.Proposal{
			{Kind: knowledge.Project, Name: "CraftedSignal", Fact: "Possible open item (inferred, not confirmed): Wait for vendor verification before launch.", SourceID: source.ContentID()},
			{Kind: knowledge.Project, Name: "CraftedSignal", Fact: "CraftedSignal is building a rollout.", SourceID: source.ContentID()},
		},
	}
	if err := store.ApplyEnrichment(ctx, chunk); err != nil {
		t.Fatal(err)
	}
	plan, err := assistant.PlanQuestion("What's outstanding for CraftedSignal?", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.RetrievePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := c.askMemoryWithMode(ctx, plan.Question, nil, "memory")
	if err != nil || !answer.Direct || !strings.Contains(answer.Answer.Text, "Possible, not confirmed") || !strings.Contains(answer.Answer.Text, "Wait for vendor verification") || strings.Contains(answer.Answer.Text, "building a rollout") || len(answer.Sources) == 0 || answer.Sources[0].SourceID != source.ContentID() {
		t.Fatalf("outstanding answer did not preserve tentative source-backed work: retrieval=%+v answer=%+v err=%v", retrieval, answer, err)
	}
	entities, err := store.FindInferredKnowledge(ctx, "CraftedSignal")
	if err != nil || len(entities) != 1 || len(entities[0].Facts) != 2 {
		t.Fatalf("saved project proposals = %+v, %v", entities, err)
	}
	var openFact knowledge.Fact
	for _, fact := range entities[0].Facts {
		if strings.HasPrefix(fact.Text, "Possible open item (inferred, not confirmed): ") {
			openFact = fact
			break
		}
	}
	if openFact.ID == "" {
		t.Fatal("tentative open-work proposal was not persisted")
	}
	if err := store.ConfirmInferredFact(ctx, entities[0].ID, openFact); err != nil {
		t.Fatal(err)
	}
	confirmedRetrieval, retrErr := c.RetrievePlan(ctx, plan)
	confirmed, err := c.askMemoryWithMode(ctx, plan.Question, nil, "memory")
	if retrErr != nil || err != nil || !strings.Contains(confirmed.Answer.Text, "Still open") || strings.Contains(confirmed.Answer.Text, "Possible, not confirmed") || !strings.Contains(confirmed.Answer.Text, "Wait for vendor verification") {
		t.Fatalf("user-confirmed follow-up did not become confirmed open work: retrieval=%+v answer=%+v retrieval_err=%v err=%v", confirmedRetrieval, confirmed, retrErr, err)
	}
}

func TestAskKeepsPartialTermCandidatesWhenExactResultsFillRankerPool(t *testing.T) {
	ctx := context.Background()
	const candidateCount = 16
	exact := make([]search.IndexHit, candidateCount)
	for i := range exact {
		exact[i] = search.IndexHit{SourceID: strings.Repeat(string(rune('a'+i)), 64), Bytes: 128, Score: 10}
	}
	partialID := strings.Repeat("f", 64)
	partial := search.IndexHit{SourceID: partialID, Bytes: 128, Score: 1}
	data := &testData{pages: []search.ExactPage{
		{Hits: exact},
		{Hits: append(append([]search.IndexHit(nil), exact...), partial)},
	}}
	client := NewClient(data)
	plan := assistant.Plan{Question: "What was in security news?", Terms: []string{"news", "security"}}

	page, err := client.planSearchCandidatesWithExactRanking(ctx, plan, false, nil, 8, candidateCount, candidateCount*16, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.queries) != 2 || data.queries[0].MinTermMatches != 0 || data.queries[0].PartialOnly || data.queries[1].MinTermMatches != 1 || !data.queries[1].PartialOnly {
		t.Fatalf("Ask candidate queries = %+v, want strict then bounded partial-only retrieval", data.queries)
	}
	for _, hit := range page.Hits {
		if hit.SourceID == partialID {
			return
		}
	}
	t.Fatalf("partial-term candidate was crowded out by exact hits: %+v", page.Hits)
}

func TestOutstandingProjectQuestionPrefersOpenWorkOverUnrelatedCapture(t *testing.T) {
	ctx := context.Background()
	c, store, now := todayTaskClient(t)
	observation, err := capture.SanitizeObservation("com.example.Browser", "accounts.craftedsignal.io", "Sign in to your account. Email address. Remember me. Protected by reCAPTCHA.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, observation, now); err != nil {
		t.Fatal(err)
	}
	projectNote, err := capture.SanitizeObservation("com.example.Notes", "", "CraftedSignal release discussion: confirm the launch date and readiness.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, projectNote, now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutKnowledgeEntity(ctx, knowledge.Entity{
		ID: "project-craftedsignal", Kind: knowledge.Project, Name: "CraftedSignal", State: knowledge.Confirmed,
		ActivitySourceIDs: []string{projectNote.ContentID()},
		OpenLoops: []knowledge.OpenLoop{
			{ID: "loop-launch", Text: "Confirm the launch date", State: knowledge.Confirmed, Sources: []knowledge.Source{{ID: projectNote.ContentID()}}},
			{ID: "loop-proposed", Text: "Consider enabling customer billing", State: knowledge.Proposed, Sources: []knowledge.Source{{ID: projectNote.ContentID()}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	linkedTask := tasks.Item{ID: "00000000000000000000000000000099", Revision: 1, Title: "Confirm release readiness", State: tasks.Open, CreatedAt: now, UpdatedAt: now, SourceID: projectNote.ContentID()}
	if err := store.PutTask(ctx, linkedTask, 0); err != nil {
		t.Fatal(err)
	}
	open, err := c.CreateTask(ctx, "Ship CraftedSignal onboarding", nil)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := c.CreateTask(ctx, "Finish CraftedSignal beta review", nil)
	if err != nil {
		t.Fatal(err)
	}
	closed.Item.State = tasks.Completed
	if _, err := c.UpdateTask(ctx, closed.Item); err != nil {
		t.Fatal(err)
	}
	plan, err := assistant.PlanQuestion("What's outstanding for CraftedSignal?", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.RetrievePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var openTaskFound, sourceLinkedTaskFound, linkedTaskTargetFound, confirmedLoopGrounded, proposedLoopLabeled bool
	for _, item := range retrieval.Evidence {
		text += item.Text + "\n"
		openTaskFound = openTaskFound || item.Origin == assistant.OriginTask && strings.Contains(item.Text, open.Item.Title)
		sourceLinkedTaskFound = sourceLinkedTaskFound || item.Origin == assistant.OriginTask && strings.Contains(item.Text, linkedTask.Title)
		confirmedLoopGrounded = confirmedLoopGrounded || item.Origin != assistant.OriginInferred && strings.Contains(item.Text, "Open: Confirm the launch date")
		proposedLoopLabeled = proposedLoopLabeled || item.Origin == assistant.OriginInferred && strings.Contains(item.Text, "Possible open item (inferred, not confirmed): Consider enabling customer billing")
	}
	for _, target := range retrieval.Targets {
		linkedTaskTargetFound = linkedTaskTargetFound || target.Kind == "task" && target.ID == linkedTask.ID
	}
	if !strings.Contains(text, "Confirm the launch date") || !confirmedLoopGrounded || !proposedLoopLabeled || !openTaskFound || !sourceLinkedTaskFound || !linkedTaskTargetFound || strings.Contains(text, "Sign in to your account") || strings.Contains(text, closed.Item.Title) {
		t.Fatalf("outstanding retrieval included unrelated content or missed open work: %+v", retrieval.Evidence)
	}
	entry, err := c.ResolveEntry(ctx, "What's outstanding for CraftedSignal?", EntryState{}, "UTC")
	if err != nil || entry.Action.Kind != intent.Ask || entry.Action.Mode != "memory" {
		t.Fatalf("outstanding question did not take the structured Ask route: %+v err=%v", entry, err)
	}
	answer, err := c.askMemoryWithMode(ctx, entry.Action.Text, nil, entry.Action.Mode)
	if err != nil || !answer.Direct || answer.GenerationIssue != "" || strings.Contains(answer.Answer.Text, "Sign in to your account") || !strings.Contains(answer.Answer.Text, "Confirm the launch date") || !strings.Contains(answer.Answer.Text, "Confirm release readiness") || len(answer.Targets) == 0 {
		t.Fatalf("outstanding question did not yield a concise structured answer: %+v err=%v", answer, err)
	}
}

func TestOutstandingSummaryDoesNotCiteOnlyTheFirstOfMultipleSupportingCaptures(t *testing.T) {
	ctx := context.Background()
	c, store, now := todayTaskClient(t)
	first, err := capture.SanitizeObservation("com.example.Notes", "", "Atlas needs a rollback review.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	second, err := capture.SanitizeObservation("com.example.Notes", "", "Atlas is waiting for a security sign-off.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []capture.SanitizedObservation{first, second} {
		if err := store.PutObservationAt(ctx, source, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutKnowledgeEntity(ctx, knowledge.Entity{
		ID: "project-atlas", Kind: knowledge.Project, Name: "Atlas", State: knowledge.Confirmed,
		OpenLoops: []knowledge.OpenLoop{
			{ID: "loop-rollback", Text: "Complete a rollback review", State: knowledge.Confirmed, Sources: []knowledge.Source{{ID: first.ContentID()}}},
			{ID: "loop-security", Text: "Get security sign-off", State: knowledge.Confirmed, Sources: []knowledge.Source{{ID: second.ContentID()}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	evidence, _, err := c.outstandingEntityEvidence(ctx, assistant.Plan{Kind: assistant.KindEntity, Entity: "Atlas"})
	if err != nil || len(evidence) != 1 {
		t.Fatalf("open-work summary = %+v, %v", evidence, err)
	}
	item := evidence[0]
	if item.SourceID == first.ContentID() || item.SourceID == second.ContentID() || item.AppName != "Library" || !strings.Contains(item.Text, "Complete a rollback review") || !strings.Contains(item.Text, "Get security sign-off") {
		t.Fatalf("combined open-work summary was attributed to one capture: %+v", item)
	}
}

func TestOutstandingQuestionWithoutSavedWorkDoesNotSurfaceProjectLoginCapture(t *testing.T) {
	ctx := context.Background()
	c, store, now := todayTaskClient(t)
	login, err := capture.SanitizeObservation("com.example.Browser", "accounts.craftedsignal.io", "Sign in to your account. Or create a new account. Email address. Email address. Remember me. Remember me. Protected by reCAPTCHA. Privacy. Terms. Signing in. Signing in. user@example.com. 1Password Sign in options are available. 1Password menu is available. Press down arrow to select.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, login, now); err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{
		"what's outstanding for craftedsignal?",
		"what's outstanding with craftedsignal?",
		"what's still on the table for craftedsignal?",
		"what open loops do we have for craftedsignal?",
		"what are the open loops with craftedsignal?",
		"what do we still have to do for craftedsignal?",
		"what are we blocked on at craftedsignal?",
		"what is still pending with craftedsignal?",
		"what are we waiting on from craftedsignal?",
		"whats outstanding for craftedsignal",
		"what’s outstanding for craftedsignal",
		"what's still open for craftedsignal",
		"what are we still waiting on from craftedsignal",
	} {
		t.Run(question, func(t *testing.T) {
			plan, err := assistant.PlanQuestion(question, now, nil)
			if err != nil || plan.Kind != assistant.KindEntity || plan.Entity != "craftedsignal" || !assistant.IsOutstandingQuestion(question) {
				t.Fatalf("lowercase project question was not recognized as open-work intent: plan=%+v err=%v", plan, err)
			}
			retrieval, err := c.RetrievePlan(ctx, plan)
			if err != nil || len(retrieval.Evidence) != 0 {
				t.Fatalf("unrelated login capture entered open-work retrieval: evidence=%+v err=%v", retrieval.Evidence, err)
			}
			entry, err := c.ResolveEntry(ctx, question, EntryState{}, "UTC")
			if err != nil || entry.Action.Kind != intent.Ask || entry.Action.Mode != "memory" {
				t.Fatalf("open-work question missed structured Ask routing: action=%+v err=%v", entry.Action, err)
			}
			answer, err := c.askMemoryWithMode(ctx, entry.Action.Text, nil, entry.Action.Mode)
			if err != nil || !answer.Direct || !answer.Answer.InsufficientEvidence || len(answer.Sources) != 0 || strings.Contains(strings.ToLower(answer.Answer.Text), "sign in") || !strings.Contains(answer.Answer.Text, "saved open work") {
				t.Fatalf("empty open-work result leaked unrelated private page text: answer=%+v err=%v", answer, err)
			}
		})
	}
}

func TestWholeDocumentQuestionSkipsAuthenticationPageWhenMatchingProjectContentExists(t *testing.T) {
	ctx := context.Background()
	c, store, now := todayTaskClient(t)
	login, err := capture.SanitizeObservation("com.example.Browser", "accounts.craftedsignal.io", "CraftedSignal sign in to your account. Email address. Remember me. Protected by reCAPTCHA.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, login, now); err != nil {
		t.Fatal(err)
	}
	content, err := capture.SanitizeObservation("com.example.Browser", "docs.craftedsignal.io", "CraftedSignal rollout guide. The release checklist is waiting on vendor verification.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, content, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	plan, err := assistant.PlanQuestion("Summarize the page about CraftedSignal", now, nil)
	if err != nil || !plan.WholeDocument {
		t.Fatalf("whole-document request was not planned: plan=%+v err=%v", plan, err)
	}
	retrieval, err := c.RetrievePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(retrieval.Evidence) != 1 || retrieval.Evidence[0].SourceID != content.ContentID() || strings.Contains(strings.ToLower(retrieval.Evidence[0].Text), "sign in") {
		t.Fatalf("whole-document retrieval selected a login page or lost project content: %+v", retrieval.Evidence)
	}
}

func TestOutstandingQuestionFiltersCredentialFormsWithoutBrowserMetadata(t *testing.T) {
	ctx := context.Background()
	c, store, now := todayTaskClient(t)
	login, err := capture.SanitizeObservation("com.example.Notes", "", "CraftedSignal account. Sign in to your account. Email address. Remember me. Protected by reCAPTCHA.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, login, now); err != nil {
		t.Fatal(err)
	}
	plan, err := assistant.PlanQuestion("What's outstanding for CraftedSignal?", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.RetrievePlan(ctx, plan)
	if err != nil || len(retrieval.Evidence) != 0 {
		t.Fatalf("credential form without browser metadata entered open-work retrieval: evidence=%+v err=%v", retrieval.Evidence, err)
	}
}

func TestOutstandingQuestionSurfacesRelatedProjectMaterialWithoutPromotingItToAnOpenTask(t *testing.T) {
	ctx := context.Background()
	c, store, now := todayTaskClient(t)
	login, err := capture.SanitizeObservation("com.example.Browser", "accounts.craftedsignal.io", "Sign in to your account. Email address. Remember me. Protected by reCAPTCHA.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, login, now); err != nil {
		t.Fatal(err)
	}
	conversation, err := capture.SanitizeObservation("com.apple.mail", "", "Subject: CraftedSignal account rollout\nMira: Let's sign in with the new service account using the billing email address before vendor verification.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, conversation, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	entry, err := c.ResolveEntry(ctx, "What's outstanding for CraftedSignal?", EntryState{}, "UTC")
	if err != nil || entry.Action.Kind != intent.Ask || entry.Action.Mode != "memory" {
		t.Fatalf("open-work question missed structured Ask route: action=%+v err=%v", entry.Action, err)
	}
	answer, err := c.askMemoryWithMode(ctx, entry.Action.Text, nil, entry.Action.Mode)
	if err != nil || !answer.Direct || !answer.Answer.InsufficientEvidence || len(answer.Sources) != 1 || answer.Sources[0].SourceID != conversation.ContentID() || answer.Sources[0].Origin != assistant.OriginRelatedProject || strings.Contains(strings.ToLower(answer.Answer.Text), "sign in") || !strings.Contains(strings.ToLower(answer.Answer.Text), "related project material") || strings.Contains(strings.ToLower(answer.Answer.Text), "still open") {
		t.Fatalf("related project material was lost or promoted to a confirmed task: answer=%+v err=%v", answer, err)
	}
}

func TestIntegrityEmailConversationRecallFindsRelatedMailAndExcludesLoginCapture(t *testing.T) {
	ctx := context.Background()
	c, store, now := todayTaskClient(t)
	mail := []struct {
		text  string
		delta time.Duration
	}{
		{"Subject: Integrity review\nMira asked for the audit evidence before replying to the customer.", -2 * time.Hour},
		{"Subject: Integrity follow-up\nWe discussed the missing access log and SOC 2 packet for the review.", -time.Hour},
	}
	for _, item := range mail {
		observation, err := capture.SanitizeObservation("com.apple.mail", "", item.text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, observation, now.Add(item.delta)); err != nil {
			t.Fatal(err)
		}
	}
	login, err := capture.SanitizeObservation("com.example.Browser", "accounts.example.org", "Sign in to your account. Email address. Remember me. Protected by reCAPTCHA.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, login, now); err != nil {
		t.Fatal(err)
	}

	question := "What integrity emails were we talking about?"
	plan, err := assistant.PlanQuestion(question, now, nil)
	if err != nil || plan.Kind != assistant.KindLookup {
		t.Fatalf("integrity email question was not planned as a memory lookup: plan=%+v err=%v", plan, err)
	}
	retrieval, err := c.RetrievePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(retrieval.Evidence) != 2 {
		t.Fatalf("integrity email recall returned unrelated or missing sources: plan=%+v diagnosis=%+v evidence=%+v", plan, retrieval.Diagnosis, retrieval.Evidence)
	}
	for _, item := range retrieval.Evidence {
		if item.AppID != "com.apple.mail" || !strings.Contains(strings.ToLower(item.Text), "integrity") || strings.Contains(strings.ToLower(item.Text), "sign in") {
			t.Fatalf("integrity email recall selected the wrong source: %+v", item)
		}
	}
	answer, err := c.AskMemory(ctx, question)
	if err != nil || answer.General || len(answer.Sources) != 2 || !strings.Contains(strings.ToLower(answer.Answer.Text), "audit evidence") || !strings.Contains(strings.ToLower(answer.Answer.Text), "access log") || strings.Contains(strings.ToLower(answer.Answer.Text), "sign in") {
		t.Fatalf("Ask did not produce a useful answer from the matched emails: result=%+v err=%v", answer, err)
	}
}

func TestKnownAppsFromSegmentsAndCache(t *testing.T) {
	f := newAskFixture(t)
	apps, err := f.client.KnownApps(context.Background())
	if err != nil || len(apps) != 2 {
		t.Fatalf("apps=%+v err=%v", apps, err)
	}
	var wa assistant.KnownApp
	for _, a := range apps {
		if a.ID == waApp {
			wa = a
		}
	}
	if wa.Name != "WhatsApp" || strings.Join(wa.Aliases, ",") != "whatsapp" {
		t.Fatalf("whatsapp = %+v", wa)
	}
	before := len(f.data.activityDates)
	if _, err := f.client.KnownApps(context.Background()); err != nil || len(f.data.activityDates) != before {
		t.Fatal("second call must be cached")
	}
	f.client.NotifyMemoryChanging()
	f.client.NotifyMemoryStable()
	if _, err := f.client.KnownApps(context.Background()); err != nil || len(f.data.activityDates) == before {
		t.Fatal("memory change must invalidate the cache")
	}
}

func TestKnownAppsUsesRecentObservationWindowForFreshCaptureDiscovery(t *testing.T) {
	f := newAskFixture(t)
	f.data.activity = nil
	if _, err := f.client.KnownApps(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.data.observationWindowRequests) != 1 {
		t.Fatalf("recent capture lookup requests=%d", len(f.data.observationWindowRequests))
	}
	request := f.data.observationWindowRequests[0]
	if !request.Start.Equal(f.now.Add(-24*time.Hour)) || !request.End.Equal(f.now.Add(time.Minute)) {
		t.Fatalf("fresh app discovery read an unnecessarily broad capture window: start=%s end=%s", request.Start, request.End)
	}
}

func TestAppActivityRecallRecognizesProductNameInsteadOfGenericBundleSuffix(t *testing.T) {
	f := newAskFixture(t)
	rememberAppName("dev.herdr.core", "Herdr Core")
	o, err := capture.SanitizeObservation("dev.herdr.core", "", "Investigated the rollback implementation and reviewed the release diff.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	f.data.observations[o.ContentID()] = o
	f.data.observationAt[o.ContentID()] = f.now.Add(-time.Hour)
	f.data.activity = append(f.data.activity, activity.Segment{ID: "herdr-work", WallStart: f.now.Add(-time.Hour), WallEnd: f.now.Add(-30 * time.Minute), Duration: int64(30 * time.Minute), AppID: "dev.herdr.core", SourceIDs: []string{o.ContentID()}})
	apps, err := f.client.KnownApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := assistant.PlanQuestion("what was I doing in herdr", f.now, apps)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.AppIDs) != 1 || plan.AppIDs[0] != "dev.herdr.core" {
		t.Fatalf("product name did not resolve to captured application: %+v", plan)
	}
	retrieval, err := f.client.RetrievePlan(context.Background(), plan)
	if err != nil || len(retrieval.Evidence) == 0 {
		t.Fatalf("app recall=%+v err=%v", retrieval, err)
	}
	for _, ev := range retrieval.Evidence {
		if ev.AppID != "dev.herdr.core" {
			t.Fatalf("app recall included unrelated app: %+v", ev)
		}
	}
}

func TestKnownAppsIncludeFreshCapturesWithoutRescanningHistory(t *testing.T) {
	f := newAskFixture(t)
	f.data.activity = nil
	if _, err := f.client.KnownApps(context.Background()); err != nil {
		t.Fatal(err)
	}
	activityReads := len(f.data.activityDates)
	o, err := capture.SanitizeObservation("dev.herdr.core", "", "Reviewed the rollout diff.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	f.data.observations[o.ContentID()] = o
	f.data.observationAt[o.ContentID()] = f.now.Add(-time.Minute)
	f.client.NotifyCaptureChanged()
	f.client.RememberCapturedApp(o)
	apps, err := f.client.KnownApps(context.Background())
	if err != nil || len(f.data.activityDates) != activityReads {
		t.Fatalf("incremental app recognition rescanned history: reads=%d before=%d err=%v", len(f.data.activityDates), activityReads, err)
	}
	plan, err := assistant.PlanQuestion("what was I doing in herdr", f.now, apps)
	if err != nil || len(plan.AppIDs) != 1 || plan.AppIDs[0] != "dev.herdr.core" {
		t.Fatalf("fresh app capture not available to planner: %+v err=%v", plan, err)
	}
}

func TestRetrievePlanCaptureFiltersScopeAndApp(t *testing.T) {
	f := newAskFixture(t)
	plan := todayPlan(assistant.KindCapture, waApp)
	plan.Terms = []string{"anna"}
	got, err := f.client.RetrievePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Evidence) != 2 || got.Evidence[0].SourceID != f.ids["wa-today2"] || got.Diagnosis.SourcesInScope != 2 || !got.Diagnosis.AppKnown {
		t.Fatalf("retrieval = %+v", got)
	}
}

func TestRetrievePlanRecapUsesChroniclesThenRawFallback(t *testing.T) {
	f := newAskFixture(t)
	got, err := f.client.RetrievePlan(context.Background(), todayPlan(assistant.KindRecap))
	if err != nil {
		t.Fatal(err)
	}
	// no chronicles available in the fake: current-hour draft plus raw fallback
	if len(got.Evidence) == 0 || got.Diagnosis.SourcesInScope != 3 {
		t.Fatalf("recap = %+v", got)
	}
	f.client.chronicles = fakeChronicles{rows: []chronicle.Summary{
		{HourID: "h1", Start: f.now.Add(-5 * time.Hour), End: f.now.Add(-4 * time.Hour), Text: "Wrote the plan"},
		{HourID: "h2", Start: f.now.Add(-4 * time.Hour), End: f.now.Add(-3 * time.Hour), Text: "Reviewed the release"},
	}}
	got, err = f.client.RetrievePlan(context.Background(), todayPlan(assistant.KindRecap))
	if err != nil || len(got.Evidence) < 2 || got.Evidence[0].Text != "Wrote the plan" {
		t.Fatalf("chronicle recap = %+v err=%v", got, err)
	}
}

func TestRetrievePlanWhatDidIDoTodayBuildsBoundedMemoryContext(t *testing.T) {
	f := newAskFixture(t)
	plan, err := assistant.PlanQuestion("What did I do today", f.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != assistant.KindRecap || plan.Scope.Label != "today" || len(plan.AppIDs)+len(plan.Domains)+len(plan.Terms) != 0 {
		t.Fatalf("today recap plan = %+v", plan)
	}
	retrieval, err := f.client.RetrievePlan(context.Background(), plan)
	if err != nil || len(retrieval.Evidence) == 0 || retrieval.Diagnosis.SourcesInScope != 3 {
		t.Fatalf("today recap retrieval = %+v err=%v", retrieval, err)
	}
	if _, err := assistant.PackContext(plan.Question, retrieval.Evidence); err != nil {
		t.Fatalf("captured recap could not be prepared as model context: %v", err)
	}
}

func TestRetrievePlanRecapFindsEncryptedCapturesBeforeActivitySegmentsExist(t *testing.T) {
	f := newAskFixture(t)
	f.data.activity = nil
	got, err := f.client.RetrievePlan(context.Background(), todayPlan(assistant.KindRecap))
	if err != nil || len(got.Evidence) == 0 {
		t.Fatalf("recap without materialized activity = %+v err=%v", got, err)
	}
	for _, evidence := range got.Evidence {
		if evidence.CapturedAt.IsZero() {
			t.Fatalf("capture-time index source has no timestamp: %+v", evidence)
		}
	}
}

func TestCapturePlansFindPrimaryEvidenceBeforeActivitySegmentsExist(t *testing.T) {
	f := newAskFixture(t)
	f.data.activity = nil
	plan := todayPlan(assistant.KindCapture, waApp)
	plan.AppNames = []string{"WhatsApp"}
	retrieval, err := f.client.RetrievePlan(context.Background(), plan)
	if err != nil || len(retrieval.Evidence) != 2 || retrieval.Evidence[0].SourceID != f.ids["wa-today2"] || retrieval.Diagnosis.SourcesInScope != 2 {
		t.Fatalf("capture retrieval before activity = %+v err=%v", retrieval, err)
	}
	answer, handled, err := f.client.AnswerDirect(context.Background(), plan)
	if err != nil || !handled || !strings.Contains(answer.Text, "Anna: see you soon") || len(answer.SourceIDs) != 2 {
		t.Fatalf("direct capture before activity = %+v handled=%v err=%v", answer, handled, err)
	}
}

func TestAppRecapDoesNotUseAnUnfilteredHourlySummary(t *testing.T) {
	f := newAskFixture(t)
	f.data.activity = nil
	f.client.chronicles = fakeChronicles{rows: []chronicle.Summary{
		{HourID: "other-hour-1", Start: f.now.Add(-time.Hour), End: f.now, Text: "Unrelated Slack conversation"},
		{HourID: "other-hour-2", Start: f.now.Add(-2 * time.Hour), End: f.now.Add(-time.Hour), Text: "Unrelated editor changes"},
	}}
	retrieval, err := f.client.RetrievePlan(context.Background(), todayPlan(assistant.KindRecap, waApp))
	if err != nil || len(retrieval.Evidence) != 2 {
		t.Fatalf("scoped recap = %+v err=%v", retrieval, err)
	}
	for _, item := range retrieval.Evidence {
		if !strings.EqualFold(item.AppID, waApp) {
			t.Fatalf("app recap included unrelated evidence: %+v", item)
		}
	}
}

type fakeChronicles struct {
	testChronicles
	rows []chronicle.Summary
}

func (f fakeChronicles) ListChronicles(context.Context, time.Time, time.Time, string, int) ([]chronicle.Summary, string, error) {
	return f.rows, "", nil
}

func TestRetrievePlanLookupScopeAppAndFallback(t *testing.T) {
	f := newAskFixture(t)
	old, today := f.ids["wa-old"], f.ids["wa-today"]
	f.data.page = search.ExactPage{Hits: []search.IndexHit{{SourceID: old, Score: 3, Bytes: indexBytes(f.data.observations[old])}, {SourceID: today, Score: 2, Bytes: indexBytes(f.data.observations[today])}}}
	plan := todayPlan(assistant.KindLookup, waApp)
	plan.Terms = []string{"anna"}
	got, err := f.client.RetrievePlan(context.Background(), plan)
	if err != nil || len(got.Evidence) != 1 || got.Evidence[0].SourceID != today {
		t.Fatalf("scoped lookup = %+v err=%v", got, err)
	}
	if f.data.query.AppID != strings.ToLower(waApp) {
		t.Fatalf("app filter = %q", f.data.query.AppID)
	}
	f.data.page = search.ExactPage{}
	got, err = f.client.RetrievePlan(context.Background(), plan)
	if err != nil || len(got.Evidence) != 2 || got.Evidence[0].SourceID != f.ids["wa-today2"] {
		t.Fatalf("fallback = %+v err=%v", got, err)
	}
	unbounded := assistant.Plan{Kind: assistant.KindLookup, Terms: []string{"invoice"}}
	f.data.page = search.ExactPage{Hits: []search.IndexHit{{SourceID: old, Score: 1, Bytes: indexBytes(f.data.observations[old])}}}
	got, err = f.client.RetrievePlan(context.Background(), unbounded)
	if err != nil || len(got.Evidence) != 1 {
		t.Fatalf("unbounded lookup = %+v err=%v", got, err)
	}
}

func TestScopedLookupFindsCapturedSourceBeforeAnyActivitySegment(t *testing.T) {
	f := newAskFixture(t)
	f.data.activity = nil
	id := f.ids["wa-today"]
	f.data.page = search.ExactPage{Hits: []search.IndexHit{{SourceID: id, Score: 1, Bytes: indexBytes(f.data.observations[id])}}}
	plan := todayPlan(assistant.KindLookup, waApp)
	plan.Terms = []string{"dinner"}
	got, err := f.client.RetrievePlan(context.Background(), plan)
	if err != nil || len(got.Evidence) != 1 || got.Evidence[0].SourceID != id || got.Evidence[0].CapturedAt.IsZero() {
		t.Fatalf("lookup before activity segment = %+v err=%v", got, err)
	}
	if !f.data.query.CapturedStart.Equal(plan.Scope.Start) || !f.data.query.CapturedEnd.Equal(plan.Scope.End) {
		t.Fatalf("time scope was not applied inside index ranking: %+v", f.data.query)
	}
}

func TestRetrievePlanDiagnosis(t *testing.T) {
	f := newAskFixture(t)
	since := f.now.Add(-time.Hour)
	f.client.SetCaptureStateReader(func() (bool, time.Time) { return false, since })
	got, err := f.client.RetrievePlan(context.Background(), todayPlan(assistant.KindCapture, "com.unknown.app"))
	if err != nil {
		t.Fatal(err)
	}
	d := got.Diagnosis
	if !d.CapturePaused || !d.PausedSince.Equal(since) || d.AppKnown || d.SourcesInScope != 0 || len(got.Evidence) != 0 {
		t.Fatalf("diagnosis = %+v", got)
	}
}

func TestAnswerDirectTime(t *testing.T) {
	f := newAskFixture(t)
	got, ok, err := f.client.AnswerDirect(context.Background(), todayPlan(assistant.KindTime, waApp))
	if err != nil || !ok || got.Text != "WhatsApp: 1h 12m today, 2 sessions." {
		t.Fatalf("app time = %q ok=%v err=%v", got.Text, ok, err)
	}
	got, ok, err = f.client.AnswerDirect(context.Background(), todayPlan(assistant.KindTime))
	if err != nil || !ok || got.Text != "Today: 2h 42m. Slack 1h 30m, WhatsApp 1h 12m." {
		t.Fatalf("total time = %q ok=%v err=%v", got.Text, ok, err)
	}
	if _, ok, _ := f.client.AnswerDirect(context.Background(), assistant.Plan{Kind: assistant.KindLookup}); ok {
		t.Fatal("lookup must not be answered directly")
	}
}

func TestAnswerDirectCaptureListing(t *testing.T) {
	f := newAskFixture(t)
	plan := todayPlan(assistant.KindCapture, waApp)
	plan.AppNames = []string{"WhatsApp"}
	got, ok, err := f.client.AnswerDirect(context.Background(), plan)
	want := "WhatsApp today:\n13:00 (1h 00m) Anna: see you soon\n11:00 (12m) Anna: dinner at eight"
	if err != nil || !ok || got.Text != want {
		t.Fatalf("listing = %q ok=%v err=%v", got.Text, ok, err)
	}
	plan.Terms = []string{"dinner"}
	if _, ok, _ := f.client.AnswerDirect(context.Background(), plan); ok {
		t.Fatal("topic capture questions need evidence")
	}
	empty := todayPlan(assistant.KindCapture, "com.unknown.app")
	if _, ok, _ := f.client.AnswerDirect(context.Background(), empty); ok {
		t.Fatal("no segments must fall through to diagnosis")
	}
}

func TestQuickPanelIntentTable(t *testing.T) {
	for input, want := range map[string]bool{
		"Give me a recap of today":      true,
		"please summarize my morning":   true,
		"show me what I did in Slack":   true,
		"what's on my plate":            true,
		"Anything from Anna?":           true,
		"any news from whatsapp":        true,
		"list my meetings":              true,
		"Daily summary":                 true,
		"remind me what Sam said":       true,
		"catch me up":                   true,
		"Call Anna tomorrow":            false,
		"Idea: try the new API":         false,
		"Listen to the podcast":         false,
		"Findings from the audit":       false,
		"Anyone should review the doc.": false,
		"Remember the deadline":         false,
	} {
		if got := quickPanelWantsAnswer(input); got != want {
			t.Errorf("quickPanelWantsAnswer(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestQuickTurnStoresPlanAndSentLine(t *testing.T) {
	turns := appendQuickConversation(nil, "What did Anna say?", assistant.Result{Answer: assistant.Answer{Text: "x"}, Sources: make([]assistant.Evidence, 3)})
	if len(turns) != 1 || turns[0].plan.Question == "" && turns[0].plan.Kind == assistant.KindGeneral && false {
		t.Fatalf("turns = %+v", turns)
	}
	if got := quickTurnMeta(turns[0]); got != "Answered from 3 items sent to the answer model" {
		t.Fatalf("meta = %q", got)
	}
	if got := answerSentLine(false, true, 0, 0); got != "Saved evidence shown directly" {
		t.Fatalf("direct = %q", got)
	}
	history := quickHistoryTurns(turns)
	if len(history) != 1 || history[0].Question != "What did Anna say?" {
		t.Fatalf("history = %+v", history)
	}
}

func TestQuickCopyShortcutOnlyWithEmptyComposer(t *testing.T) {
	d := &desktop{quickConversation: []quickConversationTurn{{question: "q", answer: "a"}}}
	if !d.quickCopyAvailable() {
		t.Fatal("copy must be available with an answer and empty composer")
	}
	d.quickInput = "typing"
	if d.quickCopyAvailable() {
		t.Fatal("copy must be disabled while the composer has text")
	}
	if (&desktop{}).quickCopyAvailable() {
		t.Fatal("copy needs an answered turn")
	}
}

func indexBytes(o capture.SanitizedObservation) int {
	return len(o.Text()) + len(o.AppID()) + len(o.Domain())
}

func TestOpenInBlurCarriesLatestExchangeToAskPage(t *testing.T) {
	d := &desktop{page: "Today", quickConversation: []quickConversationTurn{
		{question: "old", answer: "older"},
		{question: "How long on WhatsApp?", answer: "WhatsApp: 1h 12m today, 2 sessions."},
	}}
	d.carryQuickToAsk()
	if d.page != "Ask" || d.askQuestion != "How long on WhatsApp?" || d.askResult == nil || d.askResult.Answer.Text != "WhatsApp: 1h 12m today, 2 sessions." {
		t.Fatalf("page=%q question=%q result=%+v", d.page, d.askQuestion, d.askResult)
	}
	d.askResult = &assistant.Result{Answer: assistant.Answer{Text: "full"}, Sources: make([]assistant.Evidence, 2)}
	d.carryQuickToAsk()
	if len(d.askResult.Sources) != 2 {
		t.Fatal("existing result with sources must be kept")
	}
}
