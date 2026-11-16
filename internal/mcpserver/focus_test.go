package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func focusCall(t *testing.T, tool string, args map[string]any, result string) (map[string]any, bool) {
	t.Helper()
	session, ctx := parityTestSession(t, &staticRPCReader{result: json.RawMessage(result)})
	out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if out.IsError {
		return nil, false
	}
	encoded, err := json.Marshal(out.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded, true
}

func first(t *testing.T, decoded map[string]any, key string) map[string]any {
	t.Helper()
	list, _ := decoded[key].([]any)
	if len(list) != 1 {
		t.Fatalf("%s = %v", key, decoded[key])
	}
	return list[0].(map[string]any)
}

const focusJSON = `"focused_role":"AXTextField","focused_name":"Subject","focused_path":"Compose > Header"`

func TestMCPToolsPassFocusBreadcrumbThrough(t *testing.T) {
	snapshots, ok := focusCall(t, "get_snapshots", map[string]any{"source_ids": []string{paritySourceID}, "max_bytes_per_source": 16},
		`{"snapshots":[{"source_id":"`+paritySourceID+`","app_id":"reader.app","content":"0123456789abcdef","offset_bytes":0,"next_offset_bytes":16,"complete":false,`+focusJSON+`}],"truncated":false}`)
	if !ok {
		t.Fatal("get_snapshots rejected the breadcrumb")
	}
	working, ok := focusCall(t, "get_working_memory", map[string]any{"hours": 4, "limit": 5},
		`{"start":"2026-11-16T08:00:00Z","end":"2026-11-16T12:00:00.000000001Z","items":[{"source_id":"`+paritySourceID+`","app_id":"a.app","captured_at":"2026-11-16T11:00:00Z","content":"text","truncated":false,`+focusJSON+`}],"truncated":false}`)
	if !ok {
		t.Fatal("get_working_memory rejected the breadcrumb")
	}
	session, ok := focusCall(t, "read_session", map[string]any{"app": "Slack", "from": 1763000000, "to": 1763003600},
		`{"app_ids":["a.app"],"from":1763000000,"to":1763003600,"captures":[{"source_id":"`+paritySourceID+`","app_id":"a.app","last_seen_at":"2026-11-16T11:00:00Z","content":"text","offset_bytes":0,"next_offset_bytes":4,"complete":true,`+focusJSON+`}],"has_more":false}`)
	if !ok {
		t.Fatal("read_session rejected the breadcrumb")
	}
	for name, item := range map[string]map[string]any{
		"get_snapshots":      first(t, snapshots, "snapshots"),
		"get_working_memory": first(t, working, "items"),
		"read_session":       first(t, session, "captures"),
	} {
		if item["focused_role"] != "AXTextField" || item["focused_name"] != "Subject" || item["focused_path"] != "Compose > Header" {
			t.Errorf("%s item = %v", name, item)
		}
	}
}

func TestMCPToolsRejectOversizedOrMalformedFocusBreadcrumb(t *testing.T) {
	long := strings.Repeat("x", maxFocusFieldBytes+1)
	if _, ok := focusCall(t, "get_snapshots", map[string]any{"source_ids": []string{paritySourceID}, "max_bytes_per_source": 4},
		`{"snapshots":[{"source_id":"`+paritySourceID+`","app_id":"reader.app","content":"0123","offset_bytes":0,"next_offset_bytes":4,"complete":false,"focused_name":"`+long+`"}],"truncated":false}`); ok {
		t.Fatal("oversized focused_name accepted")
	}
	if _, ok := focusCall(t, "read_session", map[string]any{"app": "Slack", "from": 1763000000, "to": 1763003600},
		`{"app_ids":["a.app"],"from":1763000000,"to":1763003600,"captures":[{"source_id":"`+paritySourceID+`","app_id":"a.app","last_seen_at":"2026-11-16T11:00:00Z","content":"text","offset_bytes":0,"next_offset_bytes":4,"complete":true,"focused_path":"a\u0000b"}],"has_more":false}`); ok {
		t.Fatal("NUL in focused_path accepted")
	}
}

func TestMCPOutputSchemasDescribeFocusBreadcrumb(t *testing.T) {
	session, ctx := parityTestSession(t, &staticRPCReader{})
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		var key string
		switch tool.Name {
		case "get_snapshots":
			key = "snapshots"
		case "get_working_memory":
			key = "items"
		case "read_session":
			key = "captures"
		default:
			continue
		}
		encoded, _ := json.Marshal(tool.OutputSchema)
		var schema map[string]any
		if err := json.Unmarshal(encoded, &schema); err != nil {
			t.Fatal(err)
		}
		items := schema["properties"].(map[string]any)[key].(map[string]any)["items"].(map[string]any)
		properties := items["properties"].(map[string]any)
		for _, name := range []string{"focused_role", "focused_name", "focused_path"} {
			if properties[name] == nil {
				t.Errorf("%s schema lacks %s", tool.Name, name)
			}
		}
		if items["additionalProperties"] != false {
			t.Errorf("%s schema must stay closed", tool.Name)
		}
	}
}
