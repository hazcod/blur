package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPSearchReturnsSummaryRowsAndFlagsClosestMatches(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	serverSide, clientSide := mcp.NewInMemoryTransports()
	var asked json.RawMessage
	server := New(searchMetadataReader(func(_ context.Context, _ string, params json.RawMessage) (json.RawMessage, error) {
		asked = params
		return json.RawMessage(`{"results":[` +
			`{"source_id":"source-one","app_id":"com.tinyspeck.slackmacgap","window_title":"Dima Vermeulen","domain":"app.slack.com","captured_at":"2026-11-15T13:47:00Z","last_seen_at":"2026-11-15T13:49:00Z","sighting_count":2,"excerpt":"…the Orfit external retest is scheduled for Thursday…","matched_terms":["dima","retest"]},` +
			`{"source_id":"source-two","app_id":"com.tinyspeck.slackmacgap","captured_at":"2026-11-14T09:40:00Z","excerpt":"Dima: morning!","matched_terms":["dima"],"partial_match":true}],` +
			`"summaries":[{"id":"chronicle:day:2026-11-15","level":"day","period":"2026-11-15","start":"2026-11-14T23:00:00Z","end":"2026-11-15T23:00:00Z","excerpt":"- Sunday: the retest scope came in from Dima.","source_ids":["source-one"]},` +
			`{"id":"chronicle:day:2026-11-14","level":"day","period":"2026-11-14","start":"2026-11-13T23:00:00Z","end":"2026-11-14T23:00:00Z","excerpt":"derived text without any source","source_ids":[]}],` +
			`"relaxed":true,"truncated":false}`), nil
	}))
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, serverSide) }()
	t.Cleanup(func() { cancel(); <-done }) //nolint:errcheck // Cleanup waits for the canceled server; assertions inspect the RPC result.
	client, err := mcp.NewClient(&mcp.Implementation{Name: "summary-test"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(client.Close)
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "search_memory", Arguments: map[string]any{"query": "dima retest"}})
	if err != nil || result.IsError {
		t.Fatalf("search failed: %+v %v", result, err)
	}
	if string(asked) != `{"query":"dima retest","limit":20}` {
		t.Fatalf("the default limit is 20, RPC got %s", asked)
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	var output struct {
		Results []struct {
			WindowTitle   string   `json:"window_title"`
			SightingCount int64    `json:"sighting_count"`
			LastSeenAt    string   `json:"last_seen_at"`
			Excerpt       string   `json:"excerpt"`
			MatchedTerms  []string `json:"matched_terms"`
			PartialMatch  bool     `json:"partial_match"`
		} `json:"results"`
		Summaries []struct {
			ID        string   `json:"id"`
			Level     string   `json:"level"`
			Period    string   `json:"period"`
			SourceIDs []string `json:"source_ids"`
		} `json:"summaries"`
		Relaxed bool `json:"relaxed"`
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Results) != 2 || output.Results[0].WindowTitle != "Dima Vermeulen" || output.Results[0].SightingCount != 2 || output.Results[0].LastSeenAt == "" || len(output.Results[0].MatchedTerms) != 2 || output.Results[0].PartialMatch || !output.Results[1].PartialMatch || !output.Relaxed {
		t.Fatalf("result rows lost fields: %s", encoded)
	}
	if len(output.Summaries) != 1 || output.Summaries[0].ID != "chronicle:day:2026-11-15" || output.Summaries[0].Level != "day" || output.Summaries[0].Period != "2026-11-15" || len(output.Summaries[0].SourceIDs) != 1 {
		t.Fatalf("a summary row needs source IDs and a level/period for get_chronicle; the one without was not dropped: %s", encoded)
	}
	text, _ := result.Content[0].(*mcp.TextContent)
	if text == nil || !strings.Contains(text.Text, "get_chronicle") || !strings.Contains(text.Text, "get_snapshots") || !strings.Contains(text.Text, "partial_match") {
		t.Fatalf("summary text must steer the host: %+v", result.Content)
	}
}
