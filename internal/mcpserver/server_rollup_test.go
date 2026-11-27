package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hazcod/blur/internal/localapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPGetChronicleReadsDayAndWeekRecapsAndRejectsBadPeriods(t *testing.T) {
	const sourceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	reader := &staticRPCReader{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = New(reader).Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	reader.result = json.RawMessage(`{"found":true,"level":"day","period":"2026-11-15","start":"2026-11-14T23:00:00Z","end":"2026-11-15T23:00:00Z","text":"- Reviewed the migration rollout.","child_ids":["hour:20261115T09Z"],"source_ids":["` + sourceID + `"],"generated_at":"2026-11-16T01:00:00Z","dirty":true}`)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_chronicle", Arguments: map[string]any{"level": "day", "period": "2026-11-15"}})
	if err != nil || result.IsError || reader.calls != 1 || reader.method != localapi.RPCGetChronicle || string(reader.params) != `{"level":"day","period":"2026-11-15"}` {
		t.Fatalf("day recap result=%+v calls=%d params=%s err=%v", result, reader.calls, reader.params, err)
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	var output struct {
		Found     bool     `json:"found"`
		Level     string   `json:"level"`
		Text      string   `json:"text"`
		ChildIDs  []string `json:"child_ids"`
		SourceIDs []string `json:"source_ids"`
		Dirty     bool     `json:"dirty"`
	}
	if json.Unmarshal(encoded, &output) != nil || !output.Found || output.Level != "day" || output.Text == "" || len(output.ChildIDs) != 1 || len(output.SourceIDs) != 1 || !output.Dirty {
		t.Fatalf("day output = %s", encoded)
	}

	// A response that does not match the request, or that carries text without
	// source IDs, is refused.
	reader.result = json.RawMessage(`{"found":true,"level":"week","period":"2026-W46","start":"2026-11-08T23:00:00Z","end":"2026-11-15T23:00:00Z","text":"- Week.","child_ids":["day:2026-11-15"],"source_ids":["` + sourceID + `"],"generated_at":"2026-11-16T01:00:00Z"}`)
	mismatch, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_chronicle", Arguments: map[string]any{"level": "day", "period": "2026-11-15"}})
	if err != nil || !mismatch.IsError {
		t.Fatalf("mismatched recap was accepted: %+v err=%v", mismatch, err)
	}
	reader.result = json.RawMessage(`{"found":true,"level":"day","period":"2026-11-15","start":"2026-11-14T23:00:00Z","end":"2026-11-15T23:00:00Z","text":"- No sources.","child_ids":["hour:20261115T09Z"],"generated_at":"2026-11-16T01:00:00Z"}`)
	unsourced, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_chronicle", Arguments: map[string]any{"level": "day", "period": "2026-11-15"}})
	if err != nil || !unsourced.IsError {
		t.Fatalf("a recap without source IDs was accepted: %+v err=%v", unsourced, err)
	}

	calls := reader.calls
	for name, arguments := range map[string]map[string]any{
		"unknown level":        {"level": "quarter", "period": "2026-Q4"},
		"day without period":   {"level": "day"},
		"bad calendar day":     {"level": "day", "period": "2026-02-30"},
		"week period for day":  {"level": "day", "period": "2026-W46"},
		"day period for week":  {"level": "week", "period": "2026-11-15"},
		"week out of range":    {"level": "week", "period": "2026-W54"},
		"non-53-week year":     {"level": "week", "period": "2027-W53"},
		"hour id with day":     {"level": "day", "period": "2026-11-15", "hour_id": "hour:20261115T10Z"},
		"hour with period":     {"level": "hour", "hour_id": "hour:20261115T10Z", "period": "2026-11-15"},
		"hour level no hourid": {"level": "hour"},
		"nothing":              {"period": "2026-11-15"},
	} {
		invalid, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_chronicle", Arguments: arguments})
		if err != nil || !invalid.IsError {
			t.Errorf("%s: result=%+v err=%v", name, invalid, err)
		}
	}
	if reader.calls != calls {
		t.Fatalf("invalid requests reached the local API: %d extra calls", reader.calls-calls)
	}
}
