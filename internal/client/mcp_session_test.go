package client

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/localapi"
)

func sessionTestCapture(t *testing.T, corpus *scenarioCorpus, app, text string, at time.Time) capture.SanitizedObservation {
	t.Helper()
	observation, err := capture.SanitizeObservation(app, "", text, capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := corpus.store.PutObservationAt(context.Background(), observation, at.UTC()); err != nil {
		t.Fatal(err)
	}
	return observation
}

type sessionTestPage struct {
	Captures []struct {
		SourceID   string `json:"source_id"`
		CapturedAt string `json:"captured_at"`
		LastSeenAt string `json:"last_seen_at"`
	} `json:"captures"`
	HasMore         bool   `json:"has_more"`
	Next            string `json:"next"`
	CoverageLimited bool   `json:"coverage_limited"`
}

func readSessionTestPage(t *testing.T, corpus *scenarioCorpus, params string) sessionTestPage {
	t.Helper()
	raw, err := corpus.client.HandleMCPRead(context.Background(), localapi.RPCReadSession, json.RawMessage(params))
	if err != nil {
		t.Fatalf("%s: %v", params, err)
	}
	var page sessionTestPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestReadSessionOrdersByFirstCaptureNotLastSeen(t *testing.T) {
	corpus := newScenarioCorpus(t)
	app := "com.example.sessionorder"
	day := time.Date(2026, 11, 12, 0, 0, 0, 0, corpus.loc)
	early := sessionTestCapture(t, corpus, app, "first captured at nine, revisited in the afternoon", day.Add(9*time.Hour))
	middle := sessionTestCapture(t, corpus, app, "first captured at eleven", day.Add(11*time.Hour))
	if err := corpus.store.RecordObservationSighting(context.Background(), early, day.Add(14*time.Hour).UTC()); err != nil {
		t.Fatal(err)
	}
	page := readSessionTestPage(t, corpus, fmt.Sprintf(`{"app":%q,"from":%d,"to":%d}`, app, day.Unix(), day.Unix()+86400))
	if len(page.Captures) != 2 || page.Captures[0].SourceID != early.ContentID() || page.Captures[1].SourceID != middle.ContentID() {
		t.Fatalf("captures = %+v, want early (first captured 09:00, last seen 14:00) before middle", page.Captures)
	}
	first, _ := time.Parse(time.RFC3339Nano, page.Captures[0].CapturedAt)
	last, _ := time.Parse(time.RFC3339Nano, page.Captures[0].LastSeenAt)
	if !last.After(first) || !first.Equal(day.Add(9*time.Hour)) {
		t.Fatalf("timestamps captured=%s last_seen=%s", page.Captures[0].CapturedAt, page.Captures[0].LastSeenAt)
	}
}

func TestReadSessionBusyWindowKeepsOldestCaptures(t *testing.T) {
	corpus := newScenarioCorpus(t)
	app := "com.example.sessionbusy"
	day := time.Date(2026, 11, 11, 0, 0, 0, 0, corpus.loc)
	total := maxMCPSessionScan + 40
	ids := make([]string, total)
	for i := range total {
		ids[i] = sessionTestCapture(t, corpus, app, fmt.Sprintf("busy window capture number %04d", i), day.Add(time.Duration(i)*2*time.Minute)).ContentID()
	}
	base := fmt.Sprintf(`{"app":%q,"from":%d,"to":%d,"limit":20,"max_bytes_per_source":64`, app, day.Unix(), day.Unix()+86400)
	page := readSessionTestPage(t, corpus, base+`}`)
	if !page.CoverageLimited || len(page.Captures) != 20 || page.Captures[0].SourceID != ids[0] || !page.HasMore {
		t.Fatalf("first page limited=%v n=%d first=%v", page.CoverageLimited, len(page.Captures), page.Captures)
	}
	second := readSessionTestPage(t, corpus, base+fmt.Sprintf(`,"after":%q}`, page.Next))
	if len(second.Captures) != 20 || second.Captures[0].SourceID != ids[20] {
		t.Fatalf("second page starts at %v, want capture 20", second.Captures)
	}
	seen, cursor, lastID := 0, "", ""
	for range 40 {
		params := base + `}`
		if cursor != "" {
			params = base + fmt.Sprintf(`,"after":%q}`, cursor)
		}
		current := readSessionTestPage(t, corpus, params)
		seen += len(current.Captures)
		if len(current.Captures) > 0 {
			lastID = current.Captures[len(current.Captures)-1].SourceID
		}
		if !current.HasMore {
			break
		}
		cursor = current.Next
	}
	if seen != maxMCPSessionScan || lastID != ids[maxMCPSessionScan-1] {
		t.Fatalf("walked %d captures ending at %s, want %d ending at capture %d", seen, lastID, maxMCPSessionScan, maxMCPSessionScan-1)
	}
}
