package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

func brussels(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	return loc
}

func TestPlanQuestionKindAndScope(t *testing.T) {
	loc := brussels(t)
	now := time.Date(2026, 11, 11, 14, 30, 0, 0, loc) // Wednesday
	at := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, loc) }
	tests := []struct {
		q          string
		kind       Kind
		label      string
		start, end time.Time
	}{
		{"What is 2+2?", KindGeneral, "", time.Time{}, time.Time{}},
		{"How long is the Eiffel tower?", KindGeneral, "", time.Time{}, time.Time{}},
		{"how much time did I spend in Slack today?", KindTime, "today", at(11, 0, 0), now},
		{"time spent on github.com this week", KindTime, "this week", at(9, 0, 0), now},
		{"how long did I work yesterday", KindTime, "yesterday", at(10, 0, 0), at(11, 0, 0)},
		{"what did you capture this morning?", KindCapture, "this morning", at(11, 6, 0), at(11, 12, 0)},
		{"what have you seen this afternoon", KindCapture, "this afternoon", at(11, 12, 0), at(11, 18, 0)},
		{"nothing captured from WhatsApp?", KindCapture, "", time.Time{}, time.Time{}},
		{"recap of my day today", KindRecap, "today", at(11, 0, 0), now},
		{"what did I do last week", KindRecap, "last week", at(2, 0, 0), at(9, 0, 0)},
		{"summarize this evening", KindRecap, "this evening", at(11, 18, 0), at(12, 0, 0)},
		{"what was I doing tonight", KindRecap, "tonight", at(11, 18, 0), at(12, 0, 0)},
		{"what did I read in the last hour", KindLookup, "last hour", now.Add(-time.Hour), now},
		{"what did I read in the past hour", KindLookup, "last hour", now.Add(-time.Hour), now},
		{"what did I read in the last 3 hours", KindLookup, "last 3 hours", now.Add(-3 * time.Hour), now},
		{"what did I do in the last 45 minutes", KindRecap, "last 45 minutes", now.Add(-45 * time.Minute), now},
		{"what did I do in the last 7 days", KindRecap, "last 7 days", now.AddDate(0, 0, -7), now},
		{"what did I read in the last 30 days", KindLookup, "last 30 days", now.AddDate(0, 0, -30), now},
		{"what did I read this month", KindLookup, "this month", at(1, 0, 0), now},
		{"what did I read on monday", KindLookup, "on Monday", at(9, 0, 0), at(10, 0, 0)},
		{"what did I read on wednesday", KindLookup, "on Wednesday", at(11, 0, 0), at(12, 0, 0)},
		{"what did I read last wednesday", KindLookup, "on Wednesday", at(4, 0, 0), at(5, 0, 0)},
		{"what am I doing right now", KindRecap, "right now", now.Add(-30 * time.Minute), now},
		{"what was I doing at the moment", KindRecap, "right now", now.Add(-30 * time.Minute), now},
		{"what did I read earlier", KindLookup, "earlier today", at(11, 0, 0), now},
		{"what did I read recently", KindLookup, "recently", now.AddDate(0, 0, -3), now},
		{"who is Mira Jansen?", KindGeneral, "", time.Time{}, time.Time{}},
		{"what do you know about Mira Jansen", KindEntity, "", time.Time{}, time.Time{}},
		{"what did I read about TLS", KindLookup, "", time.Time{}, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			plan, err := PlanQuestion(tt.q, now, nil)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Kind != tt.kind {
				t.Fatalf("kind = %v, want %v (%+v)", plan.Kind, tt.kind, plan)
			}
			if tt.label == "" {
				if !plan.Scope.IsZero() {
					t.Fatalf("unexpected scope %+v", plan.Scope)
				}
				return
			}
			s := plan.Scope
			if s.Label != tt.label || !s.Explicit || !s.Start.Equal(tt.start) || !s.End.Equal(tt.end) {
				t.Fatalf("scope = %+v, want %s [%v, %v)", s, tt.label, tt.start, tt.end)
			}
			if s.Start.Location().String() != loc.String() {
				t.Fatalf("scope location %v", s.Start.Location())
			}
		})
	}
}

func TestLatestSecurityNewsIsATopicLookupWithoutFreshnessExactTerm(t *testing.T) {
	plan, err := PlanQuestion("latest security news", time.Date(2026, 11, 15, 14, 0, 0, 0, time.UTC), nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, ok := memoryLookupCandidate(plan, nil)
	if !ok {
		t.Fatal("bare topic was not considered for personal memory lookup")
	}
	if plan.Kind != KindLookup || plan.Entity != "" || strings.Join(plan.Terms, " ") != "news security" {
		t.Fatalf("latest topic was not reduced to historical lookup terms: %+v", plan)
	}
}

func TestRetrievalCommandsKeepMemoryIntentWithoutMatchingSources(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	for _, question := range []string{"Find voyagerpacket", "Search for voyagerpacket", "look up voyagerpacket", "Pull up voyagerpacket", "please find voyagerpacket", "Could you search for voyagerpacket?"} {
		plan, err := PlanQuestion(question, now, nil)
		if err != nil || plan.Kind != KindLookup || !plan.MatchOnly || strings.Join(plan.Terms, " ") != "voyagerpacket" {
			t.Fatalf("search imperative lost memory intent: %q plan=%+v err=%v", question, plan, err)
		}
	}
	for _, question := range []string{"Findings about voyagerpacket", "Explain how to find voyagerpacket", "What is TLS?", "Find a way to solve the equation", "Find out why the sky is blue", "Search the web for restaurants", "Search the internet for restaurants"} {
		plan, err := PlanQuestion(question, now, nil)
		if err != nil || plan.Kind != KindGeneral {
			t.Fatalf("non-command became a memory search: %q plan=%+v err=%v", question, plan, err)
		}
	}
}

func TestStoredRecordLocationsKeepMemoryIntentWithoutMatchingSources(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	for _, question := range []string{
		"Where is the record for voyagerpacket?",
		"Where's that record of voyagerpacket?",
		"Where are the records about voyagerpacket?",
		"Where is our record on voyagerpacket?",
	} {
		plan, err := PlanQuestion(question, now, nil)
		if err != nil || plan.Kind != KindLookup || !plan.MatchOnly || strings.Join(plan.Terms, " ") != "voyagerpacket" || !QuestionSeeksPersonalMemory(question) {
			t.Fatalf("stored record location lost memory intent: %q plan=%+v err=%v", question, plan, err)
		}
	}
	for _, question := range []string{"Where is the record player made?", "What is the world record for running?", "Where are records manufactured?", "Where is Paris?"} {
		plan, err := PlanQuestion(question, now, nil)
		if err != nil || plan.Kind != KindGeneral {
			t.Fatalf("general location became a memory search: %q plan=%+v err=%v", question, plan, err)
		}
	}
}

func TestSearchStoredRecordLocationKeepsQuotedTopic(t *testing.T) {
	for _, question := range []string{"Where are the records about voyagerpacket?", `Where are the records about "voyagerpacket"?`} {
		plan, err := PlanSearch(question, time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC), nil)
		if err != nil || !plan.MatchOnly || strings.Join(plan.Terms, " ") != "voyagerpacket" {
			t.Fatalf("stored-record grammar narrowed the topic: %q plan=%+v err=%v", question, plan, err)
		}
	}
	plan, err := PlanQuestion("Where are the records about", time.Now(), nil)
	if err != nil || plan.Kind != KindGeneral {
		t.Fatalf("incomplete record prefix became a personal lookup: %+v %v", plan, err)
	}
}

func TestWholeDocumentRequestsAreExplicitAndKeepTopicTerms(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	whole := []struct {
		question string
		terms    []string
	}{
		{"Summarize the whole page about TLS", []string{"tls"}},
		{"Summarize the article about TLS", []string{"tls"}},
		{"Can you summarise this entire article on certificate rotation?", []string{"certificate", "rotation"}},
		{"Give me an overview of the full document about project Orion.", []string{"project"}},
		{"Give me the gist of this transcript about TLS", []string{"tls"}},
		{"What are the key points in the Northstar report?", []string{"northstar"}},
		{"What is this article about?", nil},
		{"What’s this webpage about?", nil},
		{"Explain this document", nil},
		{"Can you explain that page to me?", nil},
		{"Walk me through this report", nil},
		{"Give me a breakdown of this document", nil},
		{"Summarize the email about compliance", []string{"compliance"}},
		{"Explain this webpage in its entirety", nil},
		{"Review the complete report", nil},
		{"Analyze all of this captured source", nil},
		{"résume toute la page sur Atlas", []string{"atlas"}},
	}
	for _, test := range whole {
		t.Run(test.question, func(t *testing.T) {
			plan, err := PlanQuestion(test.question, now, nil)
			if err != nil || !plan.WholeDocument || plan.Kind != KindLookup {
				t.Fatalf("whole-document request plan = %+v, err=%v", plan, err)
			}
			for _, term := range plan.Terms {
				if isWholeDocumentGrammarWord(term) {
					t.Fatalf("request grammar leaked into topic terms: %+v", plan.Terms)
				}
			}
			for _, want := range test.terms {
				if !containsString(plan.Terms, want) {
					t.Fatalf("topic term %q was dropped from plan: %+v", want, plan)
				}
			}
		})
	}
	for _, question := range []string{
		"What does the second paragraph say about TLS?",
		"Summarize the paragraph about TLS",
		"What is the full path for the config file?",
		"Explain the full path for the config file",
		"What does this page say about Orion?",
		"What does the article say about TLS?",
		"Which sentence from the report mentions TLS?",
		"What was that email about?",
	} {
		plan, err := PlanQuestion(question, now, nil)
		if err != nil || plan.WholeDocument {
			t.Fatalf("passage or ordinary question was expanded: question=%q plan=%+v err=%v", question, plan, err)
		}
	}
}

func TestOutstandingProjectQuestionsUseStructuredOpenWorkIntent(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for _, question := range []string{
		"What's outstanding for CraftedSignal?",
		"What's outstanding with CraftedSignal?",
		"What's outstanding on CraftedSignal?",
		"What is outstanding at CraftedSignal?",
		"What are the outstanding items for CraftedSignal?",
		"What is still pending with CraftedSignal?",
		"whats outstanding for CraftedSignal",
		"what’s outstanding for CraftedSignal",
		"what's still open for CraftedSignal",
		"what are we still waiting on from CraftedSignal",
		"Anything left to do for CraftedSignal?",
		"What do I still owe CraftedSignal?",
		"What do we owe CraftedSignal?",
		"What are we waiting for from CraftedSignal?",
		"What are we waiting on from CraftedSignal?",
		"What follow-ups are still open for CraftedSignal?",
		"Where did we leave things with CraftedSignal?",
		"What is left to do for CraftedSignal?",
		"What do I need to do for CraftedSignal?",
		"What should I do for CraftedSignal?",
		"What have I got to do for CraftedSignal?",
		"What are the next steps for CraftedSignal?",
		"What remains to do for CraftedSignal?",
		"Any blockers for CraftedSignal?",
		"What are we waiting on for CraftedSignal?",
		"What needs follow-up for CraftedSignal?",
		"What’s on my plate for CraftedSignal?",
		"What tasks do I have for CraftedSignal?",
		"What are my tasks for CraftedSignal?",
		"Which tasks are still open for CraftedSignal?",
		"Any pending work for CraftedSignal?",
		"What’s the next step for CraftedSignal?",
	} {
		t.Run(question, func(t *testing.T) {
			plan, err := PlanQuestion(question, now, nil)
			if err != nil || plan.Kind != KindEntity || plan.Entity != "craftedsignal" || !plan.MatchOnly || strings.Join(plan.Terms, " ") != "craftedsignal" || !IsOutstandingQuestion(question) {
				t.Fatalf("outstanding question plan = %+v, err=%v", plan, err)
			}
		})
	}
	for _, question := range []string{"What's the outstanding balance?", "Show me the CraftedSignal sign-in page"} {
		if IsOutstandingQuestion(question) {
			t.Fatalf("unrelated question classified as outstanding work: %q", question)
		}
	}
	domainAlias := KnownApp{ID: "accounts.craftedsignal.io", Name: "accounts.craftedsignal.io", Aliases: []string{"craftedsignal"}, Domain: true}
	plan, err := PlanQuestion("What's outstanding for CraftedSignal?", now, []KnownApp{domainAlias})
	if err != nil || len(plan.Domains) != 0 || len(plan.AppIDs) != 0 || plan.Entity != "craftedsignal" {
		t.Fatalf("project was incorrectly scoped to a same-named website: %+v err=%v", plan, err)
	}
}

func TestOutstandingProjectQuestionUnderstandsOpenLoopParaphrases(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for _, question := range []string{
		"What's still on the table for CraftedSignal?",
		"What open loops do we have for CraftedSignal?",
		"What are the open loops with CraftedSignal?",
		"What do we still have to do for CraftedSignal?",
		"What are we blocked on at CraftedSignal?",
	} {
		t.Run(question, func(t *testing.T) {
			plan, err := PlanQuestion(question, now, nil)
			if err != nil || plan.Kind != KindEntity || plan.Entity != "craftedsignal" || !plan.MatchOnly || !IsOutstandingQuestion(question) {
				t.Fatalf("open-work paraphrase was not routed to structured retrieval: plan=%+v err=%v", plan, err)
			}
		})
	}
	for _, question := range []string{
		"What is the outstanding balance for CraftedSignal?",
		"What are the open invoices for CraftedSignal?",
		"What is the open source license for CraftedSignal?",
	} {
		if IsOutstandingQuestion(question) {
			t.Fatalf("financial status was classified as open work: %q", question)
		}
	}
}

func TestPlanQuestionAppsDomainsAndTerms(t *testing.T) {
	now := time.Date(2026, 11, 11, 14, 30, 0, 0, time.UTC)
	apps := []KnownApp{
		{ID: "com.example.Linear", Name: "Linear", Aliases: []string{"lin"}},
		{ID: "news.ycombinator.com", Name: "Hacker News", Aliases: []string{"hn"}, Domain: true},
	}
	tests := []struct {
		q       string
		ids     []string
		names   []string
		domains []string
		terms   string
	}{
		{"nothing captured from WhatsApp yet?", []string{"net.whatsapp.whatsapp"}, []string{"WhatsApp"}, nil, ""},
		{"what did I read in vs code today", []string{"com.microsoft.vscode"}, []string{"VS Code"}, nil, "read"},
		{"what did I write in Visual Studio Code", []string{"com.microsoft.vscode"}, []string{"VS Code"}, nil, "write"},
		{"I wrote my notes about taxes", nil, nil, nil, "notes taxes wrote"},
		{"what did I write in notes about taxes", []string{"com.apple.notes"}, []string{"Notes"}, nil, "taxes write"},
		{"what did I do in Linear", []string{"com.example.linear"}, []string{"Linear"}, nil, ""},
		{"what did I see on hn", nil, nil, []string{"news.ycombinator.com"}, ""},
		{"what did I read on github.com about caching", nil, nil, []string{"github.com"}, "caching read"},
		{"did I open main.go in cursor", []string{"com.todesktop.230313mzl4w4u92"}, []string{"Cursor"}, nil, "go main"},
		{"what did I read in last 7 days about tls", nil, nil, nil, "read tls"},
	}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			plan, err := PlanQuestion(tt.q, now, apps)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(plan.AppIDs, ",") != strings.Join(tt.ids, ",") || strings.Join(plan.AppNames, ",") != strings.Join(tt.names, ",") || strings.Join(plan.Domains, ",") != strings.Join(tt.domains, ",") {
				t.Fatalf("apps=%v names=%v domains=%v", plan.AppIDs, plan.AppNames, plan.Domains)
			}
			if got := strings.Join(plan.Terms, " "); got != tt.terms {
				t.Fatalf("terms = %q, want %q", got, tt.terms)
			}
		})
	}
}

func TestQuestionSearchTermsKeepTopicAndDropEmailConversationScaffolding(t *testing.T) {
	plan, err := PlanQuestion("What integrity emails were we talking about?", time.Now(), nil)
	if err != nil || plan.Kind != KindLookup || strings.Join(plan.Terms, " ") != "integrity" {
		t.Fatalf("conversation wording obscured the topic term: plan=%+v err=%v", plan, err)
	}
	plan, err = PlanQuestion("What was that email about?", time.Now(), nil)
	if err != nil || plan.Kind != KindLookup || strings.Join(plan.Terms, " ") != "email" {
		t.Fatalf("source-type cue was lost when no topic was supplied: plan=%+v err=%v", plan, err)
	}
}

func TestPlanQuestionRejectsInvalidAndIsDeterministic(t *testing.T) {
	now := time.Date(2026, 11, 11, 14, 30, 0, 0, time.UTC)
	if _, err := PlanQuestion("  ", now, nil); err == nil {
		t.Fatal("empty question accepted")
	}
	a, _ := PlanQuestion("what did I do today in slack", now, nil)
	b, _ := PlanQuestion("what did I do today in slack", now, nil)
	if a.Kind != b.Kind || a.Scope != b.Scope || a.SearchQuery() != b.SearchQuery() {
		t.Fatal("planner is not deterministic")
	}
}

func TestIsFollowUpAndInherit(t *testing.T) {
	for q, want := range map[string]bool{
		"and in Slack?": true, "what about yesterday?": true, "yesterday?": true, "who said that?": true,
		"what did I read about TLS in the last week please": false, "write a haiku": false, "": false,
	} {
		if got := IsFollowUp(q); got != want {
			t.Errorf("IsFollowUp(%q) = %v, want %v", q, got, want)
		}
	}
	now := time.Date(2026, 11, 11, 14, 30, 0, 0, time.UTC)
	prev, _ := PlanQuestion("what did I do today", now, nil)
	next, _ := PlanQuestion("and in Slack?", now, nil)
	next = followUpPlan(next, prev)
	if next.Scope.Label != "today" || next.Scope.Explicit {
		t.Fatalf("scope not inherited: %+v", next.Scope)
	}
}

type planTestRetriever struct {
	testRetriever
	apps      []KnownApp
	retrieval Retrieval
	direct    *Answer
	got       Plan
}

func (r *planTestRetriever) KnownApps(context.Context) ([]KnownApp, error) { return r.apps, nil }
func (r *planTestRetriever) RetrievePlan(_ context.Context, p Plan) (Retrieval, error) {
	r.got = p
	return r.retrieval, nil
}
func (r *planTestRetriever) AnswerDirect(_ context.Context, p Plan) (DirectAnswer, bool, error) {
	r.got = p
	if r.direct == nil {
		return DirectAnswer{}, false, nil
	}
	return DirectAnswer{Answer: *r.direct}, true, nil
}

func TestAskWithHistoryDirectAnswerSkipsModel(t *testing.T) {
	r := &planTestRetriever{direct: &Answer{Text: "2 h in Slack."}}
	g := &testGenerator{}
	svc := NewService(r, g, testCounter{tokens: 10}, 4096)
	got, err := svc.Ask(context.Background(), "how much time did I spend in Slack today?")
	if err != nil || !got.Direct || got.Answer.Text != "2 h in Slack." || g.calls != 0 || got.Plan.Kind != KindTime {
		t.Fatalf("got %+v err %v calls %d", got, err, g.calls)
	}
}

func TestAskWithHistoryEmptyEvidenceExplainsWhy(t *testing.T) {
	paused := time.Date(2026, 11, 11, 13, 10, 0, 0, time.UTC)
	for _, tt := range []struct {
		name string
		q    string
		d    Diagnosis
		want string
	}{
		{"paused", "what did I do today", Diagnosis{CapturePaused: true, PausedSince: paused}, "Capture has been paused since 13:10, so nothing new has been saved. Resume capture and ask again."},
		{"unknown app", "what did I read in whatsapp", Diagnosis{}, "I haven't captured anything from WhatsApp yet. Check Capture scope in Settings if it should be included."},
		{"empty scope", "what did I read yesterday", Diagnosis{}, "Nothing captured yesterday yet."},
		{"outstanding project work", "What's outstanding for CraftedSignal?", Diagnosis{}, "I couldn't find any saved open work for that project."},
		{"other", "what did I read about tls", Diagnosis{}, "I couldn't find that in your memory. Try naming the app, the day, or a word on screen."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &planTestRetriever{retrieval: Retrieval{Diagnosis: tt.d}}
			g := &testGenerator{}
			got, err := NewService(r, g, testCounter{tokens: 10}, 4096).Ask(context.Background(), tt.q)
			if err != nil || !got.Answer.InsufficientEvidence || got.Answer.Text != tt.want || g.calls != 0 {
				t.Fatalf("got %q err %v", got.Answer.Text, err)
			}
		})
	}
	if got := EmptyAnswerText(Plan{}, Diagnosis{CapturePaused: true}); got != "Capture is paused, so nothing new has been saved. Resume capture and ask again." {
		t.Fatal(got)
	}
}

func TestAskWithHistoryPlanRetrievalAndFollowUp(t *testing.T) {
	id := strings.Repeat("c", 64)
	r := &planTestRetriever{retrieval: Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Discussed launch.", AppName: "Slack", CapturedAt: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}}}}
	g := &testGenerator{answer: ModelAnswer{Text: "Launch was discussed.", SourceIDs: []string{id}}}
	prev, _ := PlanQuestion("what did I do yesterday", time.Now(), nil)
	svc := NewService(r, g, testCounter{tokens: 10}, 4096)
	got, err := svc.AskWithHistory(context.Background(), "and in slack?", []Turn{{Question: "q", Plan: prev}})
	if err != nil {
		t.Fatal(err)
	}
	if r.got.Scope.Label != "yesterday" || len(r.got.AppIDs) != 1 || got.SentItems != 1 || got.SentBytes == 0 || got.Plan.Kind == KindGeneral || len(got.Sources) != 1 {
		t.Fatalf("plan %+v result %+v", r.got, got)
	}
	if !strings.Contains(g.packed.User, `"app":"Slack"`) || !strings.Contains(g.packed.User, `"time":"2026-11-11T09:00:00Z"`) {
		t.Fatalf("packed context lacks app/time: %s", g.packed.User)
	}
}

func TestAskGeneralWithoutGeneralGeneratorIsClear(t *testing.T) {
	got, err := NewService(&testRetriever{}, &testGenerator{}, testCounter{tokens: 1}, 4096).Ask(context.Background(), "What is 2+2?")
	if err != nil || !got.General || got.Answer.Text != GeneralUnavailableText {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestPackContextLayersItemsAndTimes(t *testing.T) {
	loc := brussels(t)
	mk := func(c string) string { return strings.Repeat(c, 64) }
	start := time.Date(2026, 11, 11, 9, 0, 0, 0, loc)
	ev := []Evidence{{SourceID: mk("a"), Text: "raw", CapturedAt: start}, {SourceID: mk("b"), Text: "hour", Layer: LayerHour, CapturedAt: start, EndAt: start.Add(time.Hour)}, {SourceID: mk("d"), Text: "digest", Layer: LayerDigest}}
	for i := 0; i < 14; i++ {
		ev = append(ev, Evidence{SourceID: mk("e")[:63] + string(rune('0'+i%10)), Text: "more"})
	}
	got, err := PackContext("recap my day", ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SourceIDs) != MaxEvidenceItems || got.SourceIDs[0] != mk("d") || got.SourceIDs[1] != mk("b") {
		t.Fatalf("layer order/limit wrong: %d %v", len(got.SourceIDs), got.SourceIDs[:2])
	}
	if !strings.Contains(got.User, `"time":"2026-11-11T09:00:00+01:00–2026-11-11T10:00:00+01:00"`) || !strings.Contains(got.User, `"layer":"hour"`) || got.InputBytes != len(got.System)+len(got.User) {
		t.Fatalf("user = %s", got.User)
	}
	if !strings.Contains(got.System, "same language") || !strings.Contains(got.System, "chronological") {
		t.Fatal("system prompt lacks recap/language rules")
	}
}

func TestAskExplainsPendingIndexInsteadOfClaimingNothingWasCaptured(t *testing.T) {
	r := &planTestRetriever{retrieval: Retrieval{Diagnosis: Diagnosis{IndexingPending: true}}}
	g := &testGenerator{}
	svc := NewService(r, g, testCounter{tokens: 10}, 4096)
	result, err := svc.Ask(context.Background(), "What did I read about zzztailneedle?")
	if err != nil || !result.IndexingPending || !result.Answer.InsufficientEvidence || !strings.Contains(result.Answer.Text, "saved content is still being indexed") || g.calls != 0 {
		t.Fatal(result, err)
	}
	id := strings.Repeat("a", 64)
	r.retrieval.Evidence = []Evidence{{SourceID: id, Text: "A saved fact."}}
	g.answer = ModelAnswer{Text: "A saved fact.", SourceIDs: []string{id}}
	result, err = svc.Ask(context.Background(), "What did I read about zzztailneedle?")
	if err != nil || !result.IndexingPending || len(result.Sources) != 1 || g.calls != 1 {
		t.Fatal("partial answer hid coverage", result, err)
	}
}
