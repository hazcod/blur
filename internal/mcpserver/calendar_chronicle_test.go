package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPCalendarChroniclesReachLocalAPIAndKeepChildHierarchy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &staticRPCReader{}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = New(r).Run(ctx, serverTransport) }()
	s, err := mcp.NewClient(&mcp.Implementation{Name: "calendar-test"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct{ level, period, start, end, child, wrong string }{
		{"month", "2024-02", "2024-02-01T00:00:00Z", "2024-03-01T00:00:00Z", "day:2024-02-29", "week:2024-W09"},
		{"year", "2024", "2024-01-01T00:00:00Z", "2025-01-01T00:00:00Z", "month:2024-02", "day:2024-02-29"},
	} {
		response := map[string]any{"found": true, "level": tc.level, "period": tc.period, "start": tc.start, "end": tc.end, "text": "- Calendar review.", "child_ids": []string{tc.child}, "source_ids": []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, "generated_at": "2025-01-02T01:00:00Z"}
		response["coverage_limited"] = true
		r.result, _ = json.Marshal(response)
		result, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "get_chronicle", Arguments: map[string]any{"level": tc.level, "period": tc.period}})
		if err != nil || result.IsError {
			t.Fatalf("calendar recap refused by MCP schema or boundary: %s %+v %v", tc.level, result, err)
		}
		encoded, _ := json.Marshal(result.StructuredContent)
		var decoded map[string]any
		if json.Unmarshal(encoded, &decoded) != nil || decoded["coverage_limited"] != true {
			t.Fatalf("calendar coverage limit disappeared at MCP boundary: %s", encoded)
		}
		response["child_ids"] = []string{tc.wrong}
		r.result, _ = json.Marshal(response)
		result, err = s.CallTool(ctx, &mcp.CallToolParams{Name: "get_chronicle", Arguments: map[string]any{"level": tc.level, "period": tc.period}})
		if err != nil || !result.IsError {
			t.Fatalf("wrong calendar hierarchy accepted: %s %+v %v", tc.level, result, err)
		}
	}
}

func TestMCPSearchSummaryCoverageSurvivesTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &staticRPCReader{}
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = New(r).Run(ctx, st) }()
	c, err := mcp.NewClient(&mcp.Implementation{Name: "calendar-search-test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, tc := range []struct{ level, period, start, end string }{
		{"hour", "hour:20240229T09Z", "2024-02-29T09:00:00Z", "2024-02-29T10:00:00Z"},
		{"day", "2024-02-29", "2024-02-29T00:00:00Z", "2024-03-01T00:00:00Z"},
		{"week", "2024-W09", "2024-02-26T00:00:00Z", "2024-03-04T00:00:00Z"},
		{"month", "2024-02", "2024-02-01T00:00:00Z", "2024-03-01T00:00:00Z"},
		{"year", "2024", "2024-01-01T00:00:00Z", "2025-01-01T00:00:00Z"},
	} {
		level := tc.level
		r.result, _ = json.Marshal(map[string]any{
			"results": []any{}, "summaries": []any{map[string]any{
				"id": "chronicle:" + level + ":" + tc.period, "level": level, "period": tc.period,
				"start": tc.start, "end": tc.end,
				"excerpt": "Calendar activity", "source_ids": []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				"coverage_limited": true,
			}}, "truncated": false,
		})
		out, err := c.CallTool(ctx, &mcp.CallToolParams{Name: "search_memory", Arguments: map[string]any{"query": "activity in 2024"}})
		if err != nil || out.IsError {
			t.Fatalf("valid limited-coverage %s summary rejected: %+v %v", level, out, err)
		}
		encoded, _ := json.Marshal(out.StructuredContent)
		var decoded struct {
			CoverageLimited bool `json:"coverage_limited"`
			Summaries       []struct {
				CoverageLimited bool `json:"coverage_limited"`
			} `json:"summaries"`
		}
		if json.Unmarshal(encoded, &decoded) != nil || !decoded.CoverageLimited || len(decoded.Summaries) != 1 || !decoded.Summaries[0].CoverageLimited {
			t.Fatalf("%s coverage limit disappeared at MCP search boundary: %s", level, encoded)
		}
	}
}
