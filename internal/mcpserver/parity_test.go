package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/localapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const paritySourceID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func parityTestSession(t *testing.T, reader RPCReader) (*mcp.ClientSession, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = New(reader).Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, ctx
}

func TestMCPParityToolsAreDiscoverableReadOnlyAndExplainWhenToUse(t *testing.T) {
	session, ctx := parityTestSession(t, &staticRPCReader{})
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}
	for name, hint := range map[string]string{"read_session": "get_chronicle", "list_wiki": "search_wiki", "get_time_report": "not a timesheet", "get_promises": "never verified outcomes"} {
		tool := byName[name]
		if tool == nil || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint || !strings.Contains(tool.Description, hint) || !strings.Contains(tool.Description, "Use ") {
			t.Errorf("%s = %+v, want read-only tool whose description mentions %q", name, tool, hint)
		}
	}
}

func TestMCPParityToolsRejectInvalidInputBeforeCallingBlur(t *testing.T) {
	reader := &staticRPCReader{result: json.RawMessage(`{}`)}
	session, ctx := parityTestSession(t, reader)
	for _, test := range []struct {
		tool string
		args map[string]any
	}{
		{"read_session", map[string]any{"app": "Slack", "from": 1763000000, "to": 1763000000 + 86401}},
		{"read_session", map[string]any{"app": "Slack", "from": 1763000000, "to": 1763003600, "limit": 21}},
		{"read_session", map[string]any{"app": "Slack", "from": 1763003600, "to": 1763000000}},
		{"read_session", map[string]any{"app": "Slack", "from": 1763000000, "to": 1763003600, "after": "bad"}},
		{"list_wiki", map[string]any{"type": "company"}},
		{"list_wiki", map[string]any{"limit": 51}},
		{"get_time_report", map[string]any{"period": "next_year"}},
		{"get_time_report", map[string]any{"period": "today", "from": "2026-11-01", "to": "2026-11-02"}},
		{"get_time_report", map[string]any{"from": "2024-01-01", "to": "2025-02-04"}},
		{"get_promises", map[string]any{"limit": 51}},
		{"get_promises", map[string]any{"unknown": true}},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: test.tool, Arguments: test.args})
		if err != nil || !result.IsError {
			t.Errorf("%s %v accepted: result=%+v err=%v", test.tool, test.args, result, err)
		}
	}
	if reader.calls != 0 {
		t.Fatalf("invalid input reached the local API %d times", reader.calls)
	}
}

func TestMCPParityToolsReturnValidatedResults(t *testing.T) {
	for _, test := range []struct {
		tool, method, params, result string
		args                         map[string]any
	}{
		{"read_session", localapi.RPCReadSession, `{"app":"Slack","from":1763000000,"to":1763003600,"limit":10,"max_bytes_per_source":2048}`,
			`{"app_ids":["com.tinyspeck.slackmacgap"],"from":1763000000,"to":1763003600,"captures":[{"source_id":"` + paritySourceID + `","app_id":"com.tinyspeck.slackmacgap","captured_at":"2026-11-15T14:47:00Z","last_seen_at":"2026-11-15T14:49:00Z","content":"hello","offset_bytes":0,"next_offset_bytes":5,"complete":true}],"has_more":false}`,
			map[string]any{"app": "Slack", "from": 1763000000, "to": 1763003600}},
		{"list_wiki", localapi.RPCListWiki, `{"type":"person","limit":50}`,
			`{"entries":[{"id":"person-dima","kind":"person","name":"Dima","aliases":[],"summary":"Runs the retest.","fact_count":1,"open_loop_count":0}],"total":1,"truncated":false}`,
			map[string]any{"type": "person"}},
		{"get_time_report", localapi.RPCGetTimeReport, `{"period":"today"}`,
			`{"from":"2026-11-16","to":"2026-11-16","total_seconds":120,"buckets":[{"app_id":"com.apple.mail","name":"Mail","seconds":120,"by_day":{"2026-11-16":120}}],"top_segments":[{"app_id":"com.apple.mail","seconds":120,"source_ids":["` + paritySourceID + `"]}],"omitted_segments_seconds":0,"note":"Tracked foreground time, not a timesheet."}`,
			map[string]any{"period": "today"}},
		{"get_promises", localapi.RPCGetPromises, `{"query":"retest","include_closed":true,"limit":20}`,
			`{"items":[{"kind":"open_loop","id":"loop-1","text":"Send scope","state":"proposed","subject":"Dima","subject_kind":"person","due_at":null,"source_ids":["` + paritySourceID + `"]}],"has_more":false,"note":"Observed items, not verified outcomes."}`,
			map[string]any{"query": "retest", "include_closed": true}},
	} {
		reader := &staticRPCReader{result: json.RawMessage(test.result)}
		session, ctx := parityTestSession(t, reader)
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: test.tool, Arguments: test.args})
		if err != nil || result.IsError || result.StructuredContent == nil || reader.calls != 1 || reader.method != test.method || string(reader.params) != test.params {
			t.Errorf("%s result=%+v calls=%d method=%q params=%s err=%v", test.tool, result, reader.calls, reader.method, reader.params, err)
		}
	}
}

func TestMCPParityToolsRejectInvalidLocalResponses(t *testing.T) {
	for _, test := range []struct {
		tool, result string
		args         map[string]any
	}{
		{"read_session", `{"app_ids":["com.hazcod.blur"],"from":1763000000,"to":1763003600,"captures":[{"source_id":"` + paritySourceID + `","app_id":"com.hazcod.blur","last_seen_at":"2026-11-15T14:49:00Z","content":"x","offset_bytes":0,"next_offset_bytes":1,"complete":true}],"has_more":false}`, map[string]any{"app": "Blur", "from": 1763000000, "to": 1763003600}},
		{"list_wiki", `{"entries":[{"id":"bad id","kind":"person","name":"X","aliases":[],"fact_count":0,"open_loop_count":0}],"total":1,"truncated":false}`, map[string]any{}},
		{"get_time_report", `{"from":"2026-11-16","to":"2026-11-16","total_seconds":1,"buckets":[],"top_segments":[{"seconds":1,"source_ids":["short"]}],"omitted_segments_seconds":0,"note":"n"}`, map[string]any{}},
		{"get_promises", `{"items":[{"kind":"task","id":"a","text":"t","state":"open","due_at":"2026-11-16T00:00:00Z","source_ids":[]}],"has_more":false,"note":"n"}`, map[string]any{}},
		{"get_promises", `{"items":[],"has_more":false,"note":"n","extra":1}`, map[string]any{}},
	} {
		session, ctx := parityTestSession(t, &staticRPCReader{result: json.RawMessage(test.result)})
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: test.tool, Arguments: test.args})
		if err != nil || !result.IsError {
			t.Errorf("%s accepted invalid response %s: %+v err=%v", test.tool, test.result, result, err)
		}
	}
}

func TestMCPTimeReportYearAcceptsAndValidatesAllDailyBuckets(t *testing.T) {
	days := map[string]int64{}
	for at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC); at.Year() == 2024; at = at.AddDate(0, 0, 1) {
		days[at.Format("2006-01-02")] = 1
	}
	body, err := json.Marshal(map[string]any{"from": "2024-01-01", "to": "2024-12-31", "total_seconds": 366, "buckets": []any{map[string]any{"app_id": "reader.app", "name": "Reader", "seconds": 366, "by_day": days}}, "top_segments": []any{}, "omitted_segments_seconds": 366, "note": "Tracked foreground time."})
	if err != nil {
		t.Fatal(err)
	}
	reader := &staticRPCReader{result: body}
	session, ctx := parityTestSession(t, reader)
	out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_time_report", Arguments: map[string]any{"from": "2024-01-01", "to": "2024-12-31"}})
	if err != nil || out.IsError || out.StructuredContent == nil || reader.calls != 1 {
		t.Fatalf("year blocked by MCP transport: %+v %v", out, err)
	}
}
