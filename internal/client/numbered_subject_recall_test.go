package client

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestNumberedSubjectRanksItsPhraseAheadOfDateTokenCollisions(t *testing.T) {
	for _, tc := range []struct{ query, correct, other string }{
		{"What time is my route 23 bus on Tuesday 23 February 2027?", "Route 23 bus departure on Tuesday 23 February 2027 at 16:00. The boarding point is platform 8.", "Route 42 bus departure on Tuesday 23 February 2027 at 09:00. The boarding point is platform 2."},
		{"Find invoice 23 approval", "Invoice 23 approval was confirmed on 23 November 2026 by the procurement team.", "Invoice 42 approval was confirmed on 23 November 2026 by the procurement team."},
		{"invoice 23 approval", "Invoice 23 approval was confirmed on 23 November 2026 by the procurement team.", "Invoice 42 approval was confirmed on 23 November 2026 by the procurement team."},
		{"Find invoice 23 \"approval\"", "Invoice 23 approval was confirmed on 23 November 2026 by the procurement team.", "Invoice 42 approval was confirmed on 23 November 2026 by the procurement team."},
		{"Find project 2026 plan", "Project 2026 plan: the release is approved. This entry was updated in November 2026.", "Project 17 plan: the release is approved. This entry was updated in November 2026."},
		{"Who or what is project 2026?", "Project 2026 is our release rehearsal. This entry was updated in November 2026.", "Project 17 is our release rehearsal. This entry was updated in November 2026."},
		{"Find café 23 approval", "Café #23 approval was confirmed on 23 November 2026 by the procurement team.", "Café 42 approval was confirmed on 23 November 2026 by the procurement team."},
		{"Find route 23 schedule", "23 Route schedule confirmed on 23 November 2026 by the transport team.", "Route 42 schedule confirmed on 23 November 2026 by the transport team."},
		{"Find project 2026 invoice 23 approval", "Project 2026 invoice #23 approval was confirmed on 23 November 2026 by the procurement team.", "Project 17 invoice #42 approval was confirmed on 23 November 2026 by the procurement team."},
	} {
		t.Run(tc.query, func(t *testing.T) {
			ctx := context.Background()
			s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			c := NewClient(s)
			defer c.Close()
			now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
			c.askNow = func() time.Time { return now }
			var ids []string
			for i, text := range []string{tc.correct, tc.other} {
				o, err := capture.SanitizeObservation("com.apple.mail", "", text, capture.MaxObservationTextBytes)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.PutObservationAt(ctx, o, now.Add(time.Duration(i-2)*time.Hour)); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, o.ContentID())
			}
			for {
				more, err := s.IndexPendingExact(ctx, 32)
				if err != nil {
					t.Fatal(err)
				}
				if !more {
					break
				}
			}
			result, err := c.AskMemory(ctx, tc.query)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Sources) == 0 || result.Sources[0].SourceID != ids[0] || result.Sources[0].TermsRelaxed {
				t.Errorf("Ask ranked a date token ahead of the numbered subject: %+v", result)
			}
			for _, source := range result.Sources {
				if source.SourceID == ids[1] && !source.TermsRelaxed {
					t.Errorf("Ask called the other numbered subject an exact match: %+v", source)
				}
			}
			page, err := c.SearchMemoryPageContext(ctx, tc.query)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Hits) == 0 || page.Hits[0].SourceID != ids[0] || page.Hits[0].Relaxed {
				t.Errorf("Search ranked a date token ahead of the numbered subject: %+v", page)
			}
			for _, hit := range page.Hits {
				if hit.SourceID == ids[1] && !hit.Relaxed {
					t.Errorf("Search called the other numbered subject an exact match: %+v", hit)
				}
			}
			params, _ := json.Marshal(mcpSearchParams{Query: tc.query, Limit: 8})
			wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, params)
			var response mcpSearchResponse
			if err != nil || json.Unmarshal(wire, &response) != nil {
				t.Fatalf("MCP: %s %v", wire, err)
			}
			if len(response.Results) == 0 || response.Results[0].SourceID != ids[0] || response.Results[0].PartialMatch {
				t.Errorf("MCP ranked a date token ahead of the numbered subject: %s", wire)
			}
			for _, hit := range response.Results {
				if hit.SourceID == ids[1] && !hit.PartialMatch {
					t.Errorf("MCP called the other numbered subject an exact match: %s", wire)
				}
			}
			if err := s.DeleteObservationRecord(ctx, ids[0]); err != nil {
				t.Fatal(err)
			}
			remaining, err := c.AskMemory(ctx, tc.query)
			if err != nil || len(remaining.Sources) != 1 || remaining.Sources[0].SourceID != ids[1] || !remaining.Sources[0].TermsRelaxed || !strings.Contains(strings.ToLower(remaining.Answer.Text), "closest") {
				t.Errorf("a date-only collision became proof after deleting the numbered subject: %+v %v", remaining, err)
			}
			page, err = c.SearchMemoryPageContext(ctx, tc.query)
			if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != ids[1] || !page.Hits[0].Relaxed || !page.Relaxed {
				t.Errorf("Search lost closest-match quality with one source: %+v %v", page, err)
			}
			wire, err = c.HandleMCPRead(ctx, localapi.RPCSearchMemory, params)
			response = mcpSearchResponse{}
			if err != nil || json.Unmarshal(wire, &response) != nil || len(response.Results) != 1 || response.Results[0].SourceID != ids[1] || !response.Results[0].PartialMatch || !response.Relaxed {
				t.Errorf("MCP lost closest-match quality with one source: %s %v", wire, err)
			}
		})
	}
}
