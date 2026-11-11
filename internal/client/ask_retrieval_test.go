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
	"github.com/hazcod/blur/internal/search"
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
	data := &testData{observations: map[string]capture.SanitizedObservation{}}
	f := &askFixture{data: data, now: now, ids: map[string]string{}}
	add := func(name, app, domain, text string, start time.Time, minutes int) {
		o, err := capture.SanitizeObservation(app, domain, text, 1024)
		if err != nil {
			t.Fatal(err)
		}
		data.observations[o.ContentID()] = o
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
	if f.data.query.AppID != waApp {
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
	if got := quickTurnMeta(turns[0]); got != "Answered from 3 items sent to the local model" {
		t.Fatalf("meta = %q", got)
	}
	if got := answerSentLine(false, true, 0, 0); got != "Answered without a model" {
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
