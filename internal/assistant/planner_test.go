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
func (r *planTestRetriever) AnswerDirect(_ context.Context, p Plan) (Answer, bool, error) {
	r.got = p
	if r.direct == nil {
		return Answer{}, false, nil
	}
	return *r.direct, true, nil
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
		ev = append(ev, Evidence{SourceID: strings.Repeat(string(rune('e'+i%1)), 0) + mk("e")[:63] + string(rune('0'+i%10)), Text: "more"})
	}
	got, err := PackContext("recap my day", ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SourceIDs) != MaxEvidenceItems || got.SourceIDs[0] != mk("d") || got.SourceIDs[1] != mk("b") {
		t.Fatalf("layer order/limit wrong: %d %v", len(got.SourceIDs), got.SourceIDs[:2])
	}
	if !strings.Contains(got.User, `"time":"09:00–10:00"`) || !strings.Contains(got.User, `"layer":"hour"`) || got.InputBytes != len(got.System)+len(got.User) {
		t.Fatalf("user = %s", got.User)
	}
	if !strings.Contains(got.System, "same language") || !strings.Contains(got.System, "chronological") {
		t.Fatal("system prompt lacks recap/language rules")
	}
}
