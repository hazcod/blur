package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPToolsPassWindowTitleThroughAndBoundIt(t *testing.T) {
	const title = `"window_title":"Pentest planning Q4"`
	snapshots, ok := focusCall(t, "get_snapshots", map[string]any{"source_ids": []string{paritySourceID}, "max_bytes_per_source": 16},
		`{"snapshots":[{"source_id":"`+paritySourceID+`","app_id":"reader.app","content":"0123456789abcdef","offset_bytes":0,"next_offset_bytes":16,"complete":false,`+title+`}],"truncated":false}`)
	if !ok || first(t, snapshots, "snapshots")["window_title"] != "Pentest planning Q4" {
		t.Fatal("get_snapshots rejected or dropped the window title")
	}
	if _, ok := focusCall(t, "get_working_memory", map[string]any{"hours": 4, "limit": 5},
		`{"start":"2026-11-16T08:00:00Z","end":"2026-11-16T12:00:00.000000001Z","items":[{"source_id":"`+paritySourceID+`","app_id":"a.app","captured_at":"2026-11-16T11:00:00Z","content":"text","truncated":false,`+title+`}],"truncated":false}`); !ok {
		t.Fatal("get_working_memory rejected the window title")
	}
	if _, ok := focusCall(t, "read_session", map[string]any{"app": "Slack", "from": 1763000000, "to": 1763003600},
		`{"app_ids":["a.app"],"from":1763000000,"to":1763003600,"captures":[{"source_id":"`+paritySourceID+`","app_id":"a.app","last_seen_at":"2026-11-16T11:00:00Z","content":"text","offset_bytes":0,"next_offset_bytes":4,"complete":true,`+title+`}],"has_more":false}`); !ok {
		t.Fatal("read_session rejected the window title")
	}
	long := strings.Repeat("x", maxWindowTitleBytes+1)
	if _, ok := focusCall(t, "get_snapshots", map[string]any{"source_ids": []string{paritySourceID}, "max_bytes_per_source": 4},
		`{"snapshots":[{"source_id":"`+paritySourceID+`","app_id":"reader.app","content":"0123","offset_bytes":0,"next_offset_bytes":4,"complete":false,"window_title":"`+long+`"}],"truncated":false}`); ok {
		t.Fatal("oversized window_title accepted")
	}
	if _, ok := focusCall(t, "read_session", map[string]any{"app": "Slack", "from": 1763000000, "to": 1763003600},
		`{"app_ids":["a.app"],"from":1763000000,"to":1763003600,"captures":[{"source_id":"`+paritySourceID+`","app_id":"a.app","last_seen_at":"2026-11-16T11:00:00Z","content":"text","offset_bytes":0,"next_offset_bytes":4,"complete":true,"window_title":"a\u0000b"}],"has_more":false}`); ok {
		t.Fatal("NUL in window_title accepted")
	}
}

func TestMCPOutputSchemasDescribeWindowTitle(t *testing.T) {
	session, ctx := parityTestSession(t, &staticRPCReader{})
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range tools.Tools {
		key := map[string]string{"get_snapshots": "snapshots", "get_working_memory": "items", "read_session": "captures"}[tool.Name]
		if key == "" {
			continue
		}
		encoded, _ := json.Marshal(tool.OutputSchema)
		var schema map[string]any
		if err := json.Unmarshal(encoded, &schema); err != nil {
			t.Fatal(err)
		}
		properties := schema["properties"].(map[string]any)[key].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
		if properties["window_title"] == nil {
			t.Errorf("%s schema lacks window_title", tool.Name)
		}
		seen++
	}
	if seen != 3 {
		t.Fatalf("checked %d tools, want 3", seen)
	}
}
