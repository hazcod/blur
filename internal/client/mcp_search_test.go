package client

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/localapi"
)

func fakeSearchResponse(rows int, text string) func(int) mcpSearchResponse {
	return func(excerptBytes int) mcpSearchResponse {
		response := mcpSearchResponse{Results: make([]mcpSearchItem, 0, rows)}
		for i := range rows {
			response.Results = append(response.Results, mcpSearchItem{
				SourceID: strings.Repeat("a", 63) + string(rune('a'+i%6)), AppID: "com.example.editor", CapturedAt: time.Date(2026, 11, 16, 10, i, 0, 0, time.UTC),
				Excerpt: Excerpt(text, 0, 5, excerptBytes), MatchedTerms: []string{"launch"},
			})
		}
		return response
	}
}

func TestMCPBudgetShortensExcerptsBeforeDroppingRows(t *testing.T) {
	text := "launch " + strings.Repeat("checklist item and more words ", 40)
	build := fakeSearchResponse(30, text)
	full, _ := json.Marshal(build(searchExcerptBytes))
	// A budget between the all-short and all-full sizes must keep every row.
	short, _ := json.Marshal(build(minSearchExcerptBytes))
	budget := (len(full) + len(short)) / 2
	response, err := fitMCPSearchResponse(build, budget)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(response)
	if len(encoded) > budget || len(response.Results) != 30 || response.Truncated {
		t.Fatalf("expected all 30 rows with shortened excerpts: rows=%d truncated=%t bytes=%d budget=%d", len(response.Results), response.Truncated, len(encoded), budget)
	}
	longest := 0
	for _, item := range response.Results {
		longest = max(longest, len(item.Excerpt))
		if len(item.Excerpt) < minSearchExcerptBytes-2 && len(item.Excerpt) < len(text) {
			t.Fatalf("an excerpt fell below the %d byte floor: %d", minSearchExcerptBytes, len(item.Excerpt))
		}
	}
	if longest >= searchExcerptBytes {
		t.Fatalf("excerpts were not shortened: longest %d", longest)
	}
}

func TestMCPBudgetDropsRowsFromTheEndOnlyAfterExcerptsReachTheFloor(t *testing.T) {
	text := "launch " + strings.Repeat("checklist item and more words ", 40)
	build := fakeSearchResponse(30, text)
	short, _ := json.Marshal(build(minSearchExcerptBytes))
	budget := len(short) / 2
	response, err := fitMCPSearchResponse(build, budget)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(response)
	if len(encoded) > budget || !response.Truncated || len(response.Results) == 0 || len(response.Results) >= 30 {
		t.Fatalf("rows=%d truncated=%t bytes=%d budget=%d", len(response.Results), response.Truncated, len(encoded), budget)
	}
	// Rows are dropped from the end: the survivors are the highest ranked.
	want := build(minSearchExcerptBytes).Results[:len(response.Results)]
	for i, item := range response.Results {
		if item.SourceID != want[i].SourceID || item.CapturedAt != want[i].CapturedAt {
			t.Fatalf("row %d is not the %dth ranked row", i, i)
		}
		if len(item.Excerpt) < minSearchExcerptBytes-2 {
			t.Fatalf("surviving excerpt is below the floor: %d", len(item.Excerpt))
		}
	}
	if _, err := fitMCPSearchResponse(build, 8); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("a response that can never fit must be refused, got %v", err)
	}
}

func TestMCPSearchHappyPathCarriesSummariesRelaxedAndSightings(t *testing.T) {
	corpus := newScenarioCorpus(t)
	seedSearchRollups(t, corpus)
	wire, err := corpus.client.HandleMCPRead(context.Background(), localapi.RPCSearchMemory, json.RawMessage(`{"query":"dima retest","limit":30}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) > maxMCPReadOutputBytes {
		t.Fatalf("response is %d bytes", len(wire))
	}
	var response struct {
		Results []map[string]any `json:"results"`
		Summ    []map[string]any `json:"summaries"`
		Relaxed bool             `json:"relaxed"`
		Trunc   *bool            `json:"truncated"`
	}
	if err := json.Unmarshal(wire, &response); err != nil {
		t.Fatal(err)
	}
	if response.Trunc == nil || !response.Relaxed || len(response.Results) < 3 || len(response.Summ) == 0 {
		t.Fatalf("response = %s", wire)
	}
	first := response.Results[0]
	for _, field := range []string{"source_id", "app_id", "domain", "captured_at", "last_seen_at", "sighting_count", "excerpt", "matched_terms"} {
		if first[field] == nil || first[field] == "" {
			t.Fatalf("first row lacks %q: %v", field, first)
		}
	}
	if _, has := first["text"]; has {
		t.Fatal("search rows are previews: full text must not be returned")
	}
	if first["partial_match"] != nil {
		t.Fatalf("an all-terms hit must not be flagged partial: %v", first)
	}
	last := response.Results[len(response.Results)-1]
	if last["partial_match"] != true {
		t.Fatalf("closest matches are flagged partial_match: %v", last)
	}
	for _, summary := range response.Summ {
		ids, _ := summary["source_ids"].([]any)
		if summary["id"] == nil || summary["level"] == nil || summary["period"] == nil || len(ids) == 0 || len(ids) > 8 {
			t.Fatalf("summary shape: %v", summary)
		}
	}
}

func TestMCPSearchLimitBounds(t *testing.T) {
	corpus := newScenarioCorpus(t)
	for _, raw := range []string{`{"query":"pentest","limit":0}`, `{"query":"pentest","limit":31}`, `{"query":"pentest","limit":-3}`, `{"query":"pentest"}`} {
		if _, err := corpus.client.HandleMCPRead(context.Background(), localapi.RPCSearchMemory, json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{`{"query":"pentest","limit":1}`, `{"query":"pentest","limit":30}`} {
		if _, err := corpus.client.HandleMCPRead(context.Background(), localapi.RPCSearchMemory, json.RawMessage(raw)); err != nil {
			t.Errorf("rejected %s: %v", raw, err)
		}
	}
}
