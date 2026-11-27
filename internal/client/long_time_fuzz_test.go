package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

// Histories change the actual encrypted activity/source lifecycle. Expected
// durations come from inserted facts and literal zone offsets, never a report,
// recap, generated answer, or the production range planner.
func FuzzLongTrackedTime(f *testing.F) {
	for _, seed := range [][]byte{{}, {0, 1, 2, 3, 4, 5, 6}, {255, 44, 71, 89, 120}, {2, 0, 2, 1}, {6, 6, 5, 4, 3, 2}, {19, 33, 80, 166, 201, 241}, {91, 41, 17, 11, 8, 4}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		ctx := context.Background()
		h := sha256.Sum256(data)
		zones := []string{"UTC", "Asia/Kathmandu", "Europe/Brussels"}
		zone := zones[int(h[0])%len(zones)]
		t.Setenv("TZ", zone)
		year := 1998 + int(h[1])%28
		month := []time.Month{time.January, time.March, time.October, time.December}[int(h[2])%4]
		wholeYear := h[3]&1 != 0
		firstMonth, nextMonth := month, month+1
		if wholeYear {
			firstMonth, nextMonth = time.January, 13
		}
		// Whole years start/end in winter. March and October straddle the
		// independently specified Brussels offset changes in these years.
		startOffset, endOffset := 0, 0
		if zone == "Asia/Kathmandu" {
			startOffset, endOffset = 20700, 20700
		}
		if zone == "Europe/Brussels" {
			startOffset, endOffset = 3600, 3600
			if !wholeYear && month == time.March {
				endOffset = 7200
			}
			if !wholeYear && month == time.October {
				startOffset = 7200
			}
		}
		start := time.Date(year, firstMonth, 1, 0, 0, 0, 0, time.FixedZone("oracle-start", startOffset))
		end := time.Date(year, nextMonth, 1, 0, 0, 0, 0, time.FixedZone("oracle-end", endOffset))
		fromDate := fmt.Sprintf("%04d-%02d-01", year, firstMonth)
		toDate := time.Date(year, nextMonth, 0, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		period := fmt.Sprintf("in %s %d", month.String(), year)
		if wholeYear {
			period = fmt.Sprintf("in %d", year)
		}
		path := filepath.Join(t.TempDir(), "tracked-time.db")
		key := securestore.OfflineFixtureKey()
		s, err := durable.OpenDurable(path, key, durable.CreateNew)
		if err != nil {
			t.Fatal(err)
		}
		var c *Client
		newClient := func() {
			c = NewClient(s)
			c.askNow = func() time.Time { return time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC) }
		}
		newClient()
		t.Cleanup(func() { c.Close(); _ = s.Close() })
		type saved struct {
			source  capture.SanitizedObservation
			segment activity.Segment
		}
		live := map[int]saved{}
		apps := []string{"com.apple.mail", "com.apple.Safari", "com.tinyspeck.slackmacgap", "com.apple.Terminal", "reader.unknown.app", "com.hazcod.blur"}
		appNames := []string{"Mail", "Safari", "Slack", "Terminal"}
		topics := []string{"invoice", "travel", "family", "research", "maintenance", "release", "security", "appointment"}
		generation := 0
		erase := func(slot int) {
			if old, ok := live[slot]; ok {
				if err := s.DeleteObservationRecord(ctx, old.source.ContentID()); err != nil {
					t.Fatal(err)
				}
				delete(live, slot)
			}
		}
		put := func(slot int, parameter byte) {
			erase(slot)
			generation++
			app := apps[(slot+int(parameter))%len(apps)]
			domain := []string{"finance.example", "travel.example", "research.example"}[int(parameter)%3]
			text := fmt.Sprintf("Saved %s reference %x slot %d edition %d", topics[int(parameter)%len(topics)], h[:4], slot, generation)
			o, err := capture.SanitizeObservation(app, domain, text, 1024)
			if err != nil {
				t.Fatal(err)
			}
			at := []time.Time{start.Add(-30 * time.Second), start.Add(10 * time.Hour), start.Add(end.Sub(start) / 2), end.Add(-30 * time.Second), end.Add(time.Minute), end.Add(-28*24*time.Hour - 300*time.Millisecond)}[slot%6]
			duration := time.Duration(1+int(parameter)%45)*time.Minute + time.Duration(parameter)*time.Millisecond
			if err := s.PutObservationAt(ctx, o, start.AddDate(0, -2, 0)); err != nil {
				t.Fatal(err)
			}
			row := activity.Segment{ID: fmt.Sprintf("time-%d-%d", slot, generation), AppID: app, Domain: domain, Topic: topics[int(parameter)%len(topics)], WallStart: at, WallEnd: at.Add(duration), Duration: int64(duration), SourceIDs: []string{o.ContentID()}}
			if parameter%4 == 0 && duration > 2*time.Minute {
				// Exercise real sessionization across an unsampled gap. The
				// independent ledger admits only the configured two known minutes;
				// unknown persisted recovery intervals survive source erasure.
				engine := activity.New(activity.Config{MaxGap: 2 * time.Minute})
				if _, err := engine.Apply(activity.Event{Kind: activity.Seen, Wall: at, AppID: app, Domain: domain, SourceID: o.ContentID()}); err != nil {
					t.Fatal(err)
				}
				rows, err := engine.Apply(activity.Event{Kind: activity.Seen, Monotonic: int64(duration), Wall: at.Add(duration), AppID: app, Domain: domain, SourceID: o.ContentID()})
				if err != nil || len(rows) != 2 || rows[0].Duration != int64(2*time.Minute) || rows[1].AppID != "" {
					t.Fatalf("engine gap: %+v %v", rows, err)
				}
				for i, part := range rows {
					part.ID = fmt.Sprintf("%s-engine-%d", row.ID, i)
					if err := s.PutActivitySegment(ctx, part); err != nil {
						t.Fatal(err)
					}
					if i == 0 {
						row.ID = part.ID
					}
				}
				row.Duration, row.WallEnd = int64(2*time.Minute), at.Add(2*time.Minute)
			} else {
				if err := s.PutActivitySegment(ctx, row); err != nil {
					t.Fatal(err)
				}
			}
			live[slot] = saved{o, row}
		}
		label := fmt.Sprintf("%s %d", month.String(), year)
		if wholeYear {
			label = fmt.Sprintf("%d", year)
		}
		expectedPrefix := func(ns int64, app int) string {
			minutes := ns / 60000000000
			duration := "<1m"
			if minutes > 0 {
				duration = fmt.Sprintf("%dm", minutes)
			}
			if minutes >= 60 {
				duration = fmt.Sprintf("%dh %02dm", minutes/60, minutes%60)
			}
			if app >= 0 {
				return appNames[app] + ": " + duration + " "
			}
			return label + ": " + duration + "."
		}
		var previous *assistant.Turn
		previousApp := -1
		check := func() {
			var total int64
			var perApp [4]int64
			inScope := map[string]bool{}
			for _, item := range live {
				row := item.segment
				if row.AppID == "com.hazcod.blur" {
					continue
				}
				left, right := row.WallStart, row.WallEnd
				if left.Before(start) {
					left = start
				}
				if right.After(end) {
					right = end
				}
				if !right.After(left) {
					continue
				}
				ns := int64(right.Sub(left))
				total += ns
				inScope[item.source.ContentID()] = true
				for i := range perApp {
					if row.AppID == apps[i] {
						perApp[i] += ns
					}
				}
			}
			report, err := c.ActivityReportContext(ctx, fromDate, toDate, zone)
			if err != nil || report.Eligible != total {
				t.Fatalf("report year=%d month=%d zone=%s generation=%d want=%d got=%+v err=%v", year, month, zone, generation, total, report, err)
			}
			var days int64
			for _, day := range report.Days {
				days += day.Duration
			}
			if days != total {
				t.Fatalf("lost local-day duration: %d want %d", days, total)
			}
			params, _ := json.Marshal(map[string]string{"from": fromDate, "to": toDate})
			raw, err := c.HandleMCPRead(ctx, localapi.RPCGetTimeReport, params)
			if err != nil {
				t.Fatal(err)
			}
			var out mcpTimeReportResponse
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			wantSeconds := (total + 500000000) / 1000000000
			if out.TotalSeconds != wantSeconds {
				t.Fatalf("MCP %d want %d", out.TotalSeconds, wantSeconds)
			}
			for _, top := range out.TopSegments {
				for _, id := range top.SourceIDs {
					if !inScope[id] {
						t.Fatalf("MCP cited erased/outside/self activity %s", id)
					}
				}
			}
			if previous != nil {
				expected := total
				if previousApp >= 0 {
					expected = perApp[previousApp]
				}
				followup, err := c.AskMemoryWithHistory(ctx, "Make it shorter", []assistant.Turn{*previous})
				if err != nil {
					t.Fatal(err)
				}
				if expected == 0 {
					if !followup.Answer.InsufficientEvidence || len(followup.Answer.SourceIDs) != 0 || len(followup.Sources) != 0 {
						t.Fatalf("erased time survived follow-up: %+v", followup)
					}
				} else if followup.Answer.InsufficientEvidence || len(followup.Sources) != 1 || !strings.Contains(followup.Sources[0].Text, "Recorded foreground activity: "+expectedPrefix(expected, previousApp)) {
					t.Fatalf("follow-up ignored changed tracked state, want %s: %+v", expectedPrefix(expected, previousApp), followup)
				}
			}
			appChoice := int(h[4]) % 4
			if total > 0 {
				question := "What was my total screen time " + period + "?"
				expected := total
				selectedApp := -1
				if perApp[appChoice] > 0 && h[7]&1 != 0 {
					expected = perApp[appChoice]
					selectedApp = appChoice
					switch h[8] % 3 {
					case 0:
						question = "How much time did I spend in " + appNames[appChoice] + " " + period + "?"
					case 1:
						question = "How much time did I spend using " + appNames[appChoice] + " " + period + "?"
					case 2:
						question = "How much time did I spend in " + appNames[appChoice] + " during " + strings.TrimPrefix(period, "in ") + "?"
					}
				}
				result, err := c.AskMemory(ctx, question)
				want := expectedPrefix(expected, selectedApp)
				if err != nil || result.Plan.Kind != assistant.KindTime || result.Answer.InsufficientEvidence || !strings.HasPrefix(result.Answer.Text, want) {
					t.Fatalf("native time %s want %s: %+v %v", question, want, result, err)
				}
				if selectedApp >= 0 && (len(result.Plan.AppIDs) != 1 || !strings.EqualFold(result.Plan.AppIDs[0], apps[selectedApp])) {
					t.Fatalf("app qualifier lost: %+v", result.Plan)
				}
				previous = &assistant.Turn{Question: question, Plan: result.Plan, Answer: result.Answer.Text, SourceIDs: result.Answer.SourceIDs}
				previousApp = selectedApp
			}
		}
		put(0, h[5])
		put(1, h[6])
		check()
		steps := min(8, max(1, len(data)))
		for i := 0; i < steps; i++ {
			value := h[(i+7)%len(h)]
			if i < len(data) {
				value = data[i]
			}
			slot := int(h[(i+15)%len(h)]) % 6
			switch value % 7 {
			case 0, 1:
				put(slot, value)
			case 2:
				erase(slot)
			case 3:
				c.Close()
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = durable.OpenDurable(path, key, durable.OpenExisting)
				if err != nil {
					t.Fatal(err)
				}
				newClient()
			case 4:
				if item, ok := live[slot]; ok {
					err := s.PutActivitySegment(ctx, item.segment)
					var storeErr *storage.StoreError
					if !errors.As(err, &storeErr) || storeErr.Code != storage.Duplicate {
						t.Fatalf("duplicate activity changed accounting: %v", err)
					}
				}
			case 5:
				if item, ok := live[slot]; ok {
					if err := s.RecordObservationSighting(ctx, item.source, end.Add(time.Duration(i+1)*time.Minute)); err != nil {
						t.Fatal(err)
					}
				}
			case 6:
				for id := range live {
					erase(id)
				}
			}
			check()
		}
	})
}
