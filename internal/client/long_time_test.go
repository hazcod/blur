package client

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type longTimeProbe struct {
	*durable.Store
	dates     [][]string
	afterRead func()
	rowLimit  int
}

func (s *longTimeProbe) ListActivitySegmentsForDates(ctx context.Context, dates []string, limit int) ([]activity.Segment, error) {
	s.dates = append(s.dates, append([]string(nil), dates...))
	if s.rowLimit > 0 {
		limit = min(limit, s.rowLimit)
	}
	rows, err := s.Store.ListActivitySegmentsForDates(ctx, dates, limit)
	if s.afterRead != nil {
		fn := s.afterRead
		s.afterRead = nil
		fn()
	}
	return rows, err
}

func longTimeFixture(t *testing.T) (*Client, *longTimeProbe) {
	t.Helper()
	t.Setenv("TZ", "UTC")
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "time.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, row := range []struct {
		id, at, app, domain string
		minutes             int
	}{
		{"prior", "2023-12-31T23:59:00Z", "com.apple.mail", "finance.example", 2},
		{"early", "2024-01-01T09:00:00Z", "com.apple.mail", "finance.example", 10},
		{"middle", "2024-06-15T09:00:00Z", "com.apple.Safari", "finance.example", 20},
		{"split", "2024-07-16T23:59:00Z", "com.apple.mail", "finance.example", 2},
		{"late", "2024-12-31T23:50:00Z", "com.apple.mail", "finance.example", 15},
		{"self", "2024-02-01T09:00:00Z", "com.hazcod.blur", "finance.example", 60},
		{"other-domain", "2024-05-01T09:00:00Z", "com.apple.mail", "other.example", 5},
		{"other-app", "2024-08-01T09:00:00Z", "com.tinyspeck.slackmacgap", "finance.example", 7},
	} {
		// Every Mail session shares the same original; session counting cannot
		// substitute source deduplication or count artificial window pieces.
		o, err := capture.SanitizeObservation(row.app, row.domain, "Saved financial activity reference for "+row.app, 1024)
		if err != nil {
			t.Fatal(err)
		}
		at, _ := time.Parse(time.RFC3339, row.at)
		if _, found, err := s.GetObservation(context.Background(), o.ContentID()); err != nil {
			t.Fatal(err)
		} else if found {
			if err := s.RecordObservationSighting(context.Background(), o, at); err != nil {
				t.Fatal(err)
			}
		} else if err := s.PutObservationAt(context.Background(), o, at); err != nil {
			t.Fatal(err)
		}
		if err := s.PutActivitySegment(context.Background(), activity.Segment{ID: row.id, WallStart: at, WallEnd: at.Add(time.Duration(row.minutes) * time.Minute), Duration: int64(time.Duration(row.minutes) * time.Minute), AppID: row.app, Domain: row.domain, SourceIDs: []string{o.ContentID()}}); err != nil {
			t.Fatal(err)
		}
	}
	p := &longTimeProbe{Store: s}
	c := NewClient(p)
	c.askNow = func() time.Time { return time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(c.Close)
	return c, p
}

func TestLongTimeReportYearConservesBothEndsAndDays(t *testing.T) {
	c, p := longTimeFixture(t)
	got, err := c.ActivityReport("2024-01-01", "2024-12-31", "UTC")
	if err != nil {
		t.Fatal("year report unavailable:", err)
	}
	if got.Eligible != int64(55*time.Minute) {
		t.Fatalf("year total %s, want 55m", time.Duration(got.Eligible))
	}
	want := map[string]int64{"2024-01-01": int64(11 * time.Minute), "2024-05-01": int64(5 * time.Minute), "2024-06-15": int64(20 * time.Minute), "2024-07-16": int64(time.Minute), "2024-07-17": int64(time.Minute), "2024-08-01": int64(7 * time.Minute), "2024-12-31": int64(10 * time.Minute)}
	if len(got.Days) != len(want) {
		t.Fatalf("days %+v", got.Days)
	}
	for _, day := range got.Days {
		if day.Duration != want[day.Date] {
			t.Fatalf("day %+v, want %d", day, want[day.Date])
		}
	}
	for _, dates := range p.dates {
		if len(dates) > 33 {
			t.Fatalf("unbounded partition read: %d", len(dates))
		}
	}
}

func TestLongTimeNativeAskCountsOriginalSessions(t *testing.T) {
	c, _ := longTimeFixture(t)
	result, err := c.AskMemory(context.Background(), "How much time did I spend in Mail in 2024?")
	if err != nil {
		t.Fatal("native year time unavailable:", err)
	}
	if result.Plan.Kind != assistant.KindTime || result.Answer.InsufficientEvidence || !strings.Contains(result.Answer.Text, "28m") || !strings.Contains(result.Answer.Text, "5 sessions") {
		t.Fatalf("native total/session count: %+v", result)
	}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	direct, handled, err := c.AnswerDirect(context.Background(), assistant.Plan{Kind: assistant.KindTime, Scope: assistant.Scope{Start: start, End: end, Label: "in 2024"}, AppIDs: []string{"com.apple.mail"}, Domains: []string{"finance.example"}})
	if err != nil || !handled || !strings.Contains(direct.Answer.Text, "23m") || !strings.Contains(direct.Answer.Text, "4 sessions") {
		t.Fatalf("qualifiers/sessions: %+v %v %v", direct, handled, err)
	}
}

func TestLongTimeMCPYearUsesAllWindows(t *testing.T) {
	c, _ := longTimeFixture(t)
	raw, err := c.HandleMCPRead(context.Background(), localapi.RPCGetTimeReport, json.RawMessage(`{"from":"2024-01-01","to":"2024-12-31"}`))
	if err != nil {
		t.Fatal("MCP year total unavailable:", err)
	}
	var out mcpTimeReportResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.TotalSeconds != 3300 || out.From != "2024-01-01" || out.To != "2024-12-31" {
		t.Fatalf("MCP total %+v", out)
	}
	for _, bucket := range out.Buckets {
		if bucket.AppID == "com.hazcod.blur" {
			t.Fatal("counted own interface")
		}
	}
}

func TestLongTimeReportsUseResolvedFractionalLocalYear(t *testing.T) {
	c, _ := longTimeFixture(t)
	t.Setenv("TZ", "Asia/Kathmandu")
	report, err := c.ActivityReport("2024-01-01", "2024-12-31", "Asia/Kathmandu")
	if err != nil || report.Eligible != int64(46*time.Minute) {
		t.Fatalf("local year: %+v %v", report, err)
	}
	raw, err := c.HandleMCPRead(context.Background(), localapi.RPCGetTimeReport, json.RawMessage(`{"from":"2024-01-01","to":"2024-12-31"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out mcpTimeReportResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.TotalSeconds != 2760 {
		t.Fatalf("MCP local year %d", out.TotalSeconds)
	}
}
func TestLongTimeReportsConserveDSTIntervals(t *testing.T) {
	c, p := longTimeFixture(t)
	for _, tc := range []struct{ id, at, from, to string }{{"spring", "2024-03-31T00:30:00Z", "2024-03-01", "2024-03-31"}, {"autumn", "2024-10-27T00:30:00Z", "2024-10-01", "2024-10-31"}} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		if err := p.PutActivitySegment(context.Background(), activity.Segment{ID: tc.id, AppID: "reader.app", WallStart: at, WallEnd: at.Add(2 * time.Hour), Duration: int64(2 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
		report, err := c.ActivityReport(tc.from, tc.to, "Europe/Brussels")
		if err != nil || report.Eligible != int64(2*time.Hour) || len(report.Days) != 1 || report.Days[0].Duration != int64(2*time.Hour) {
			t.Fatalf("%s DST: %+v %v", tc.id, report, err)
		}
	}
}
func TestLongTimeMCPRoundsOnlyAfterBoundaryPiecesMerge(t *testing.T) {
	c, p := longTimeFixture(t)
	at := time.Date(2024, 7, 16, 23, 59, 59, 700000000, time.UTC)
	if err := p.PutActivitySegment(context.Background(), activity.Segment{ID: "subsecond", AppID: "com.apple.mail", WallStart: at, WallEnd: at.Add(600 * time.Millisecond), Duration: int64(600 * time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	raw, err := c.HandleMCPRead(context.Background(), localapi.RPCGetTimeReport, json.RawMessage(`{"from":"2024-01-01","to":"2024-12-31"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out mcpTimeReportResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.TotalSeconds != 3301 {
		t.Fatalf("split subsecond rounded before global merge: %d", out.TotalSeconds)
	}
}
func TestLongTimeMandatoryFailureReturnsNoTotal(t *testing.T) {
	for _, mode := range []string{"cancel", "row budget", "commit", "erase"} {
		t.Run(mode, func(t *testing.T) {
			c, p := longTimeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.afterRead = func() {
				switch mode {
				case "cancel":
					cancel()
				case "row budget":
					p.rowLimit = 1
				case "commit":
					at := time.Date(2024, 1, 2, 9, 0, 0, 0, time.UTC)
					if err := p.PutActivitySegment(context.Background(), activity.Segment{ID: "during-read", AppID: "reader.app", WallStart: at, WallEnd: at.Add(time.Minute), Duration: int64(time.Minute)}); err != nil {
						t.Fatal(err)
					}
				case "erase":
					o, err := capture.SanitizeObservation("com.apple.mail", "finance.example", "Saved financial activity reference for com.apple.mail", 1024)
					if err != nil {
						t.Fatal(err)
					}
					if err := p.DeleteObservationRecord(context.Background(), o.ContentID()); err != nil {
						t.Fatal(err)
					}
				}
			}
			report, err := c.ActivityReportContext(ctx, "2024-01-01", "2024-12-31", "UTC")
			if err == nil || report.Eligible != 0 || len(report.Days) != 0 {
				t.Fatalf("%s returned partial total: %+v %v", mode, report, err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if (mode == "commit" || mode == "erase") && !errors.Is(err, ErrMemoryChanging) {
				t.Fatal(err)
			}
		})
	}
}
func TestLongTimeNativeQueuedPublicationRequiresCurrentMemoryAndRange(t *testing.T) {
	for _, mode := range []string{"normal", "commit", "dates", "zone", "navigate"} {
		t.Run(mode, func(t *testing.T) {
			c, p := longTimeFixture(t)
			app := &meetingOperationTestApp{posted: make(chan func(), 16)}
			d := &desktop{app: app, client: c, page: "Time", tokens: themePalette(false), reportStart: "2024-01-01", reportEnd: "2024-12-31", reportZone: "UTC"}
			d.loadActivityReport()
			publish := nextRecapPublication(t, app)
			switch mode {
			case "commit":
				at := time.Date(2024, 1, 2, 9, 0, 0, 0, time.UTC)
				if err := p.PutActivitySegment(context.Background(), activity.Segment{ID: "queued-change", AppID: "reader.app", WallStart: at, WallEnd: at.Add(time.Minute), Duration: int64(time.Minute)}); err != nil {
					t.Fatal(err)
				}
			case "dates":
				d.reportStart = "2024-02-01"
			case "zone":
				d.reportZone = "Asia/Kathmandu"
			case "navigate":
				d.page = "Ask"
			}
			publish()
			if mode == "normal" {
				if !d.reportReady || d.report.Eligible != int64(55*time.Minute) {
					t.Fatalf("native total not installed: %+v", d.report)
				}
			} else if d.reportReady || d.report.Eligible != 0 {
				t.Fatalf("%s installed stale native total: %+v", mode, d.report)
			}
		})
	}
}

func TestLongTimeFourHundredLocalDatesAcrossOffsetChange(t *testing.T) {
	c, _ := longTimeFixture(t)
	report, err := c.ActivityReport("2023-10-01", "2024-11-03", "Europe/Brussels")
	if err != nil || report.Eligible != int64(46*time.Minute) {
		t.Fatalf("400 local dates: %+v %v", report, err)
	}
	if _, err := c.ActivityReport("2023-10-01", "2024-11-04", "Europe/Brussels"); err == nil {
		t.Fatal("401 local dates accepted")
	}
}

func TestLongTimeUnrelatedTopicCannotBecomeWholePeriodTotal(t *testing.T) {
	c, _ := longTimeFixture(t)
	result, err := c.AskMemory(context.Background(), "How much time did I spend on the unrelated Orchid project in 2024?")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Answer.InsufficientEvidence || !strings.Contains(result.Answer.Text, "app or website") || strings.Contains(result.Answer.Text, "55m") {
		t.Fatalf("unrelated topic received whole-period time: plan=%+v answer=%s", result.Plan, result.Answer.Text)
	}
}
