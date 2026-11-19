package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type searchMetadataReader func(context.Context, string, json.RawMessage) (json.RawMessage, error)

func (f searchMetadataReader) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	return f(ctx, method, params)
}

func TestMCPPreservesVisitTimePartialCoverageAndPendingIndex(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	serverSide, clientSide := mcp.NewInMemoryTransports()
	server := New(searchMetadataReader(func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"results":[{"source_id":"source-one","app_id":"reader.app","excerpt":"Mira owns the rollback.","captured_at":"2026-11-12T14:00:00Z","partial_capture":true},{"source_id":"source-two","app_id":"reader.app","excerpt":"Legacy memory with unknown capture time.","capture_time_unknown":true}],"truncated":false,"indexing_pending":true,"coverage_limited":true}`), nil
	}))
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, serverSide) }()
	t.Cleanup(func() { cancel(); <-done }) //nolint:errcheck // Cleanup waits for the canceled server; assertions inspect the RPC result.
	client, err := mcp.NewClient(&mcp.Implementation{Name: "metadata-test"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(client.Close)
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "search_memory", Arguments: map[string]any{"query": "Mira yesterday"}})
	if err != nil || result.IsError {
		t.Fatalf("valid memory metadata was rejected: %+v %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Results []struct {
			CapturedAt  string `json:"captured_at"`
			Partial     bool   `json:"partial_capture"`
			TimeUnknown bool   `json:"capture_time_unknown"`
		} `json:"results"`
		Pending bool `json:"indexing_pending"`
		Limited bool `json:"coverage_limited"`
	}
	if json.Unmarshal(encoded, &output) != nil || len(output.Results) != 2 || output.Results[0].CapturedAt != "2026-11-12T14:00:00Z" || !output.Results[0].Partial || !output.Pending || !output.Limited || !output.Results[1].TimeUnknown || output.Results[1].CapturedAt != "" {
		t.Fatalf("MCP discarded useful scope/coverage metadata: %s", encoded)
	}
}
