package client

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/storage"
)

func TestLongTimeEngineUnknownGapIsNotTrackedForeground(t *testing.T) {
	c, p := longTimeFixture(t)
	at := time.Date(2024, 9, 1, 9, 0, 0, 0, time.UTC)
	engine := activity.New(activity.Config{MaxGap: 5 * time.Minute})
	if _, err := engine.Apply(activity.Event{Kind: activity.Seen, Wall: at, AppID: "com.apple.mail"}); err != nil {
		t.Fatal(err)
	}
	rows, err := engine.Apply(activity.Event{Kind: activity.Seen, Monotonic: int64(time.Hour), Wall: at.Add(time.Hour), AppID: "com.apple.mail"})
	if err != nil || len(rows) != 2 || rows[1].AppID != "" || rows[1].Duration != int64(55*time.Minute) {
		t.Fatalf("expected real engine gap: %+v %v", rows, err)
	}
	for _, row := range rows {
		if err := p.PutActivitySegment(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
	report, err := c.ActivityReport("2024-01-01", "2024-12-31", "UTC")
	if err != nil || report.Eligible != int64(time.Hour) {
		t.Errorf("55m fixture plus 5m tracked: %+v %v", report, err)
	}
	ask, err := c.AskMemory(context.Background(), "How much time did I spend in 2024?")
	if err != nil || ask.Answer.InsufficientEvidence || !strings.Contains(ask.Answer.Text, "1h 00m") {
		t.Errorf("Ask counted unknown time/session: %+v %v", ask, err)
	}
	_, read, err := c.activityScopeReport(context.Background(), time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), time.UTC, assistant.Plan{})
	if err != nil || read.sessions != 8 {
		t.Errorf("unknown gap counted as a session: %d %v", read.sessions, err)
	}
	raw, err := c.HandleMCPRead(context.Background(), localapi.RPCGetTimeReport, json.RawMessage(`{"from":"2024-01-01","to":"2024-12-31"}`))
	var out mcpTimeReportResponse
	if err != nil || json.Unmarshal(raw, &out) != nil || out.TotalSeconds != 3600 {
		t.Errorf("MCP counted unknown gap: %+v %v", out, err)
	}
}

type longTimeHistoryProbe struct {
	*longTimeProbe
	beforeHistory func()
}

func (p *longTimeHistoryProbe) AppendQuestionAtRead(ctx context.Context, record storage.QuestionRecord, epoch uint64) (uint64, bool, error) {
	if p.beforeHistory != nil {
		p.beforeHistory()
	}
	return p.Store.AppendQuestionAtRead(ctx, record, epoch)
}

func TestLongTimeAskCannotAdoptDeletionDuringOwnHistoryWrite(t *testing.T) {
	_, p := longTimeFixture(t)
	probe := &longTimeHistoryProbe{longTimeProbe: p, beforeHistory: func() {
		o, err := capture.SanitizeObservation("com.apple.mail", "finance.example", "Saved financial activity reference for com.apple.mail", 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.DeleteObservationRecord(context.Background(), o.ContentID()); err != nil {
			t.Fatal(err)
		}
	}}
	c := NewClient(probe)
	t.Cleanup(c.Close)
	got, err := c.AskMemory(context.Background(), "How much time did I spend in 2024?")
	if err != ErrMemoryChanging || got.Answer.Text != "" {
		t.Fatalf("erased answer was retokened by history: %+v %v", got, err)
	}
	if got := c.RecentQuestions(context.Background(), 10); len(got) != 0 {
		t.Fatalf("stale answer persisted: %v", got)
	}
}

func TestLongTimeOrdinaryQuestionWording(t *testing.T) {
	c, _ := longTimeFixture(t)
	for _, tc := range []struct{ question, want string }{
		{"How much time did I spend in Mail during 2024?", "28m"},
		{"How much time did I spend using Mail in 2024?", "28m"},
		{"What was my total screen time in 2024?", "55m"},
	} {
		t.Run(tc.question, func(t *testing.T) {
			got, err := c.AskMemory(context.Background(), tc.question)
			if err != nil || got.Plan.Kind != assistant.KindTime || got.Answer.InsufficientEvidence || !strings.Contains(got.Answer.Text, tc.want) {
				t.Fatalf("supported time question rejected: %+v %v", got, err)
			}
		})
	}
}

func TestLongTimeEmptyScopeAnswersWithoutRetrievalFallback(t *testing.T) {
	c, _ := longTimeFixture(t)
	for _, question := range []string{"How much time did I spend in 2022?", "How much time did I spend in Safari in 2025?"} {
		got, err := c.AskMemory(context.Background(), question)
		if err != nil || !got.Direct || !got.Answer.InsufficientEvidence || !strings.Contains(got.Answer.Text, "No tracked foreground time") {
			t.Errorf("empty scope failed: %+v %v", got, err)
		}
	}
}

func TestLongTimeQueuedAskFencesDurableErasure(t *testing.T) {
	for _, tc := range []struct {
		name            string
		erase, followup bool
	}{
		{"unchanged-with-history", false, false}, {"erased-before-publication", true, false},
		{"followup-unchanged-with-history", false, true}, {"followup-erased-before-publication", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := longTimeFixture(t)
			app := &pausedAskApp{posted: make(chan func(), 16)}
			d := &desktop{app: app, client: c, page: "Ask", askQuestion: "How much time did I spend in 2024?"}
			if tc.followup {
				initial, err := c.AskMemory(context.Background(), d.askQuestion)
				if err != nil {
					t.Fatal(err)
				}
				d.askConversation = appendQuickConversation(nil, d.askQuestion, initial)
				d.askQuestion = "Make it shorter"
			}
			previousTurns := len(d.askConversation)
			d.askMemory()
			next := func() func() {
				t.Helper()
				select {
				case fn := <-app.posted:
					return fn
				case <-time.After(5 * time.Second):
					t.Fatal("native Ask callback missing")
					return nil
				}
			}
			next()() // initial quick-panel resize, before retrieval
			publish := next()
			if tc.erase {
				o, err := capture.SanitizeObservation("com.apple.mail", "finance.example", "Saved financial activity reference for com.apple.mail", 1024)
				if err != nil {
					t.Fatal(err)
				}
				if err := p.DeleteObservationRecord(context.Background(), o.ContentID()); err != nil {
					t.Fatal(err)
				}
				report, err := c.ActivityReport("2024-01-01", "2024-12-31", "UTC")
				if err != nil || report.Eligible != int64(32*time.Minute) {
					t.Fatalf("erasure failed: %+v %v", report, err)
				}
			}
			publish()
			for d.askLoading {
				next()()
			}
			if tc.erase {
				if d.askResult != nil || len(d.askConversation) != previousTurns || d.status == "" {
					t.Fatalf("erased result installed: %+v %q", d.askResult, d.status)
				}
			} else {
				if d.askResult == nil || !strings.Contains(d.askResult.Answer.Text, "55m") {
					t.Fatalf("own history write rejected valid total: %+v %q", d.askResult, d.status)
				}
				if got := c.RecentQuestions(context.Background(), 10); len(got) != previousTurns+1 || got[len(got)-1] != d.askQuestion {
					t.Fatalf("question history lost: %v", got)
				}
			}
		})
	}
}

func TestLongTimeSkippedMidnightUsesRequestedLocalDate(t *testing.T) {
	c, p := longTimeFixture(t)
	t.Setenv("TZ", "America/Santiago")
	at := time.Date(2024, 9, 8, 3, 30, 0, 0, time.UTC)
	if err := p.PutActivitySegment(context.Background(), activity.Segment{ID: "midnight-gap", AppID: "reader.app", WallStart: at, WallEnd: at.Add(time.Hour), Duration: int64(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		from, to string
		seconds  int64
	}{
		{"2024-09-07", "2024-09-07", 1800},
		{"2024-09-08", "2024-09-08", 1800},
		{"2024-09-07", "2024-09-08", 3600},
	} {
		report, err := c.ActivityReport(tc.from, tc.to, "America/Santiago")
		if err != nil || report.Eligible != tc.seconds*int64(time.Second) {
			t.Errorf("native %s..%s: %+v %v", tc.from, tc.to, report, err)
		}
		params, _ := json.Marshal(mcpTimeReportParams{From: tc.from, To: tc.to})
		raw, err := c.HandleMCPRead(context.Background(), localapi.RPCGetTimeReport, params)
		var out mcpTimeReportResponse
		if err != nil || json.Unmarshal(raw, &out) != nil || out.TotalSeconds != tc.seconds || out.From != tc.from || out.To != tc.to {
			t.Errorf("MCP %s..%s: %+v %v", tc.from, tc.to, out, err)
		}
	}
}
