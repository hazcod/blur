package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/localapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPExposesBoundedReadOnlyMemoryTools(t *testing.T) {
	transport := localapi.NewMemoryTransport(8, 32<<10)
	endpoint := localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: "/tmp/blur-mcp-test"}
	grants, err := localapi.NewGrantStore(nil, nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.Issue("claude.mcp", []localapi.Scope{localapi.ScopeMCPBoundedRead}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rpcDone := make(chan error, 1)
	go func() {
		rpcDone <- localapi.ServeRPC(ctx, transport, endpoint, grants, func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
			calls++
			if method != localapi.RPCSearchMemory || string(params) != `{"query":"launch","limit":2}` {
				t.Errorf("RPC received method=%q params=%s", method, params)
			}
			var response struct {
				Results   []map[string]any `json:"results"`
				Truncated bool             `json:"truncated"`
			}
			for i := range 9 {
				response.Results = append(response.Results, map[string]any{
					"source_id": fmt.Sprintf("source-%d", i), "app_id": "com.example.editor",
					"domain": "", "text": strings.Repeat("launch decision ", 150),
				})
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				return nil, err
			}
			return encoded, nil
		})
	}()
	waitForRPC(t, transport, endpoint)

	reader := localapi.NewRPCClient(transport, endpoint, grant)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	mcpServer := New(reader)
	mcpDone := make(chan error, 1)
	go func() { mcpDone <- mcpServer.Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(session.Close)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]*mcp.Tool, len(tools.Tools))
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}
	if len(tools.Tools) != 9 || byName["get_snapshots"] == nil || byName["get_chronicle"] == nil || byName["get_chronicles"] == nil || byName["get_working_memory"] == nil || byName["get_recent_activity"] == nil || byName["search_wiki"] == nil || byName["get_wiki_page"] == nil || byName["list_tasks"] == nil || byName["search_memory"] == nil {
		t.Fatalf("MCP tools = %+v, want bounded memory read tools and search_memory", byName)
	}
	if byName["search_memory"].Annotations == nil || !byName["search_memory"].Annotations.ReadOnlyHint || !byName["search_memory"].Annotations.IdempotentHint {
		t.Fatalf("search_memory annotations = %+v, want read-only and idempotent", byName["search_memory"].Annotations)
	}
	schema, ok := byName["search_memory"].InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("search_memory input schema type = %T", byName["search_memory"].InputSchema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("search_memory properties = %T", schema["properties"])
	}
	querySchema, ok := properties["query"].(map[string]any)
	if !ok || querySchema["maxLength"] != float64(maxSearchQueryBytes) {
		t.Fatalf("query schema = %#v, want byte limit %d", properties["query"], maxSearchQueryBytes)
	}
	limitSchema, ok := properties["limit"].(map[string]any)
	if !ok || limitSchema["maximum"] != float64(maxSearchResults) {
		t.Fatalf("limit schema = %#v, want maximum %d", properties["limit"], maxSearchResults)
	}
	args := map[string]any{"query": "launch", "limit": 2}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_memory", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.StructuredContent == nil || len(result.Content) == 0 {
		t.Fatalf("search result = %+v, want structured successful output", result)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Results []struct {
			SourceID  string `json:"source_id"`
			Text      string `json:"text"`
			Truncated bool   `json:"truncated"`
		} `json:"results"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Results) != 2 || output.Results[0].SourceID != "source-0" || len(output.Results[0].Text) > maxSearchTextBytes || !output.Results[0].Truncated || !output.Truncated || calls != 1 || len(encoded) > maxSearchOutputBytes {
		t.Fatalf("MCP output=%+v, authenticated RPC calls=%d", output, calls)
	}
	bad, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_memory", Arguments: map[string]any{"query": strings.Repeat("x", maxSearchQueryBytes+1)}})
	if err != nil {
		t.Fatal(err)
	}
	if !bad.IsError || calls != 1 {
		t.Fatalf("oversized query result=%+v, RPC calls=%d; want local tool error and no RPC", bad, calls)
	}
	badLimit, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_memory", Arguments: map[string]any{"query": "launch", "limit": 9}})
	if err != nil || !badLimit.IsError || calls != 1 {
		t.Fatalf("oversized result limit=%+v RPC calls=%d err=%v; want local error and no RPC", badLimit, calls, err)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "delete_memory"}); err == nil {
		t.Fatal("mutation tool unexpectedly exists")
	}
	cancel()
	for name, done := range map[string]<-chan error{"rpc": rpcDone, "mcp": mcpDone} {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("%s server exit = %v", name, err)
			}
		case <-time.After(time.Second):
			t.Errorf("%s server did not stop", name)
		}
	}
}

func TestMCPExposesPaginatedReadOnlySnapshots(t *testing.T) {
	transport := localapi.NewMemoryTransport(8, 32<<10)
	endpoint := localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: "/tmp/blur-mcp-snapshots-test"}
	grants, err := localapi.NewGrantStore(nil, nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.Issue("claude.mcp", []localapi.Scope{localapi.ScopeMCPBoundedRead}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const sourceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var calls int
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rpcDone := make(chan error, 1)
	go func() {
		rpcDone <- localapi.ServeRPC(ctx, transport, endpoint, grants, func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
			calls++
			if method != localapi.RPCGetSnapshots || string(params) != `{"source_ids":["`+sourceID+`"],"max_bytes_per_source":16}` {
				t.Errorf("RPC received method=%q params=%s", method, params)
			}
			return json.RawMessage(`{"snapshots":[{"source_id":"` + sourceID + `","app_id":"reader.app","domain":"example.test","captured_at":"2026-11-15T12:00:00Z","content":"0123456789abcdef","offset_bytes":0,"next_offset_bytes":16,"complete":false}]}`), nil
		})
	}()
	waitForRPC(t, transport, endpoint)
	reader := localapi.NewRPCClient(transport, endpoint, grant)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	mcpServer := New(reader)
	mcpDone := make(chan error, 1)
	go func() { mcpDone <- mcpServer.Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(session.Close)
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]*mcp.Tool, len(tools.Tools))
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}
	if len(tools.Tools) != 9 || byName["get_snapshots"] == nil || byName["get_chronicle"] == nil || byName["get_chronicles"] == nil || byName["get_working_memory"] == nil || byName["get_recent_activity"] == nil || byName["search_wiki"] == nil || byName["get_wiki_page"] == nil || byName["list_tasks"] == nil || byName["search_memory"] == nil {
		t.Fatalf("MCP tools = %+v, want read-only snapshot, chronicle, activity, and search tools", byName)
	}
	if byName["get_snapshots"].Annotations == nil || !byName["get_snapshots"].Annotations.ReadOnlyHint || !byName["get_snapshots"].Annotations.IdempotentHint {
		t.Fatalf("get_snapshots annotations = %+v, want read-only and idempotent", byName["get_snapshots"].Annotations)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_snapshots", Arguments: map[string]any{"source_ids": []string{sourceID}, "max_bytes_per_source": 16}})
	if err != nil || result.IsError || result.StructuredContent == nil || calls != 1 {
		t.Fatalf("get_snapshots result=%+v calls=%d err=%v", result, calls, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Snapshots []struct {
			SourceID string `json:"source_id"`
			Content  string `json:"content"`
			Next     int    `json:"next_offset_bytes"`
			Complete bool   `json:"complete"`
		} `json:"snapshots"`
	}
	if err := json.Unmarshal(encoded, &output); err != nil || len(output.Snapshots) != 1 || output.Snapshots[0].SourceID != sourceID || output.Snapshots[0].Content != "0123456789abcdef" || output.Snapshots[0].Next != 16 || output.Snapshots[0].Complete {
		t.Fatalf("snapshot chunk=%+v err=%v", output, err)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "delete_memory"}); err == nil {
		t.Fatal("mutation tool unexpectedly exists")
	}
	cancel()
	for name, done := range map[string]<-chan error{"rpc": rpcDone, "mcp": mcpDone} {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("%s server exit = %v", name, err)
			}
		case <-time.After(time.Second):
			t.Errorf("%s server did not stop", name)
		}
	}
}

func TestMCPGetChroniclesReadsBoundedRangeAndPagination(t *testing.T) {
	const first = "hour:20261115T10Z"
	const sourceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	reader := &staticRPCReader{result: json.RawMessage(`{"items":[{"hour_id":"` + first + `","start":"2026-11-15T10:00:00Z","end":"2026-11-15T11:00:00Z","summary":"Reviewed the database migration rollout.","source_ids":["` + sourceID + `"]}],"next":"` + first + `","truncated":true}`)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- New(reader).Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_chronicles", Arguments: map[string]any{"start": "2026-11-15T10:00:00Z", "end": "2026-11-15T14:00:00Z", "limit": 1}})
	if err != nil || result.IsError || result.StructuredContent == nil || reader.calls != 1 || reader.method != localapi.RPCListChronicles {
		t.Fatalf("get_chronicles result=%+v calls=%d method=%q params=%s err=%v", result, reader.calls, reader.method, reader.params, err)
	}
	var params localapi.ListChroniclesParams
	if json.Unmarshal(reader.params, &params) != nil || !params.Start.Equal(time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)) || !params.End.Equal(time.Date(2026, 11, 15, 14, 0, 0, 0, time.UTC)) || params.Limit != 1 {
		t.Fatalf("range RPC params = %s", reader.params)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	var output struct {
		Items []struct {
			HourID    string   `json:"hour_id"`
			SourceIDs []string `json:"source_ids"`
		} `json:"items"`
		Next      string `json:"next"`
		Truncated bool   `json:"truncated"`
	}
	if err != nil || json.Unmarshal(encoded, &output) != nil || len(output.Items) != 1 || output.Items[0].HourID != first || len(output.Items[0].SourceIDs) != 1 || output.Next != first || !output.Truncated {
		t.Fatalf("get_chronicles structured output = %s err=%v", encoded, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("MCP server exit = %v", err)
		}
	case <-time.After(time.Second):
		t.Error("MCP server did not stop")
	}
}

func TestMCPGetWorkingMemoryReadsRecentSourceLinkedCaptures(t *testing.T) {
	const sourceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	reader := &staticRPCReader{result: json.RawMessage(`{"start":"2026-11-16T08:00:00Z","end":"2026-11-16T12:00:00.000000001Z","items":[{"source_id":"` + sourceID + `","app_id":"com.tinyspeck.slackmacgap","captured_at":"2026-11-16T11:00:00Z","content":"#security-news CVE-2026-1234 affects Chrome.","truncated":false}],"truncated":false,"indexing_pending":false}`)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- New(reader).Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_working_memory", Arguments: map[string]any{"hours": 4, "limit": 5}})
	if err != nil || result.IsError || result.StructuredContent == nil || reader.calls != 1 || reader.method != localapi.RPCGetWorkingMemory {
		t.Fatalf("get_working_memory result=%+v calls=%d method=%q params=%s err=%v", result, reader.calls, reader.method, reader.params, err)
	}
	var params localapi.GetWorkingMemoryParams
	if json.Unmarshal(reader.params, &params) != nil || params.Hours != 4 || params.Limit != 5 {
		t.Fatalf("working-memory RPC params = %s", reader.params)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	var output struct {
		Items []struct {
			SourceID string `json:"source_id"`
			Content  string `json:"content"`
		} `json:"items"`
		Truncated bool `json:"truncated"`
	}
	if err != nil || json.Unmarshal(encoded, &output) != nil || len(output.Items) != 1 || output.Items[0].SourceID != sourceID || !strings.Contains(output.Items[0].Content, "CVE-2026-1234") {
		t.Fatalf("working-memory output = %s err=%v", encoded, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("MCP server exit = %v", err)
		}
	case <-time.After(time.Second):
		t.Error("MCP server did not stop")
	}
}

type staticRPCReader struct {
	method string
	params json.RawMessage
	result json.RawMessage
	calls  int
}

func (r *staticRPCReader) Call(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	r.calls++
	r.method = method
	r.params = append(r.params[:0], params...)
	return append(json.RawMessage(nil), r.result...), nil
}

func TestMCPGetChronicleReturnsSourceLinkedRecap(t *testing.T) {
	const hourID = "hour:20261115T10Z"
	const sourceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	reader := &staticRPCReader{result: json.RawMessage(`{"found":true,"hour_id":"` + hourID + `","start":"2026-11-15T10:00:00Z","end":"2026-11-15T11:00:00Z","summary":"Reviewed the database migration rollout.","source_ids":["` + sourceID + `"]}`)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- New(reader).Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_chronicle", Arguments: map[string]any{"hour_id": hourID}})
	if err != nil || result.IsError || result.StructuredContent == nil || reader.calls != 1 || reader.method != localapi.RPCGetChronicle || string(reader.params) != `{"hour_id":"`+hourID+`"}` {
		t.Fatalf("get_chronicle result=%+v calls=%d method=%q params=%s err=%v", result, reader.calls, reader.method, reader.params, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Found     bool     `json:"found"`
		Summary   string   `json:"summary"`
		SourceIDs []string `json:"source_ids"`
	}
	if json.Unmarshal(encoded, &output) != nil || !output.Found || output.Summary != "Reviewed the database migration rollout." || len(output.SourceIDs) != 1 || output.SourceIDs[0] != sourceID {
		t.Fatalf("chronicle output = %s", encoded)
	}
	invalid, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_chronicle", Arguments: map[string]any{"hour_id": "day:20261115"}})
	if err != nil || !invalid.IsError || reader.calls != 1 {
		t.Fatalf("invalid chronicle input result=%+v calls=%d err=%v", invalid, reader.calls, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("MCP server exit = %v", err)
		}
	case <-time.After(time.Second):
		t.Error("MCP server did not stop")
	}
}

func TestMCPGetRecentActivityReturnsCitedSegments(t *testing.T) {
	const sourceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	reader := &staticRPCReader{result: json.RawMessage(`{"segments":[{"start":"2026-11-15T12:00:00Z","end":"2026-11-15T12:01:00Z","duration_ns":60000000000,"app_id":"com.example.chat","topic":"Security news","source_ids":["` + sourceID + `"]}],"truncated":false}`)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- New(reader).Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_recent_activity", Arguments: map[string]any{}})
	if err != nil || result.IsError || result.StructuredContent == nil || reader.calls != 1 || reader.method != localapi.RPCGetRecentActivity || string(reader.params) != `{"days_back":1,"limit":5}` {
		t.Fatalf("recent activity result=%+v calls=%d method=%q params=%s err=%v", result, reader.calls, reader.method, reader.params, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Segments []struct {
			AppID     string   `json:"app_id"`
			Topic     string   `json:"topic"`
			SourceIDs []string `json:"source_ids"`
		} `json:"segments"`
	}
	if json.Unmarshal(encoded, &output) != nil || len(output.Segments) != 1 || output.Segments[0].AppID != "com.example.chat" || output.Segments[0].Topic != "Security news" || len(output.Segments[0].SourceIDs) != 1 || output.Segments[0].SourceIDs[0] != sourceID {
		t.Fatalf("recent activity output = %s", encoded)
	}
	invalid, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_recent_activity", Arguments: map[string]any{"days_back": 8}})
	if err != nil || !invalid.IsError || reader.calls != 1 {
		t.Fatalf("invalid recent activity input result=%+v calls=%d err=%v", invalid, reader.calls, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("MCP server exit = %v", err)
		}
	case <-time.After(time.Second):
		t.Error("MCP server did not stop")
	}
}

func TestMCPSearchWikiReturnsSourceLinkedFacts(t *testing.T) {
	const sourceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	reader := &staticRPCReader{result: json.RawMessage(`{"entities":[{"id":"mira-person","kind":"person","name":"Mira","aliases":["Mira L."],"state":"confirmed","facts":[{"text":"Mira owns rollback decisions.","state":"confirmed","sources":[{"id":"` + sourceID + `"}]}]}],"truncated":false}`)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- New(reader).Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_wiki", Arguments: map[string]any{"query": "Mira rollback"}})
	if err != nil || result.IsError || result.StructuredContent == nil || reader.calls != 1 || reader.method != localapi.RPCSearchWiki || string(reader.params) != `{"query":"Mira rollback","limit":5}` {
		t.Fatalf("search_wiki result=%+v calls=%d method=%q params=%s err=%v", result, reader.calls, reader.method, reader.params, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Entities []struct {
			Name  string `json:"name"`
			Facts []struct {
				Text    string `json:"text"`
				Sources []struct {
					ID string `json:"id"`
				} `json:"sources"`
			} `json:"facts"`
		} `json:"entities"`
	}
	if json.Unmarshal(encoded, &output) != nil || len(output.Entities) != 1 || output.Entities[0].Name != "Mira" || len(output.Entities[0].Facts) != 1 || output.Entities[0].Facts[0].Text != "Mira owns rollback decisions." || len(output.Entities[0].Facts[0].Sources) != 1 || output.Entities[0].Facts[0].Sources[0].ID != sourceID {
		t.Fatalf("Wiki output = %s", encoded)
	}
	invalid, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_wiki", Arguments: map[string]any{"query": " ", "limit": 5}})
	if err != nil || !invalid.IsError || reader.calls != 1 {
		t.Fatalf("invalid Wiki search input result=%+v calls=%d err=%v", invalid, reader.calls, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("MCP server exit = %v", err)
		}
	case <-time.After(time.Second):
		t.Error("MCP server did not stop")
	}
}

func TestMCPListTasksReturnsBoundedTaskAndRejectsUnknownFields(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	reader := &staticRPCReader{result: json.RawMessage(`{"items":[{"id":"` + taskID + `","revision":2,"title":"Buy eggs","state":"open","created_at":"2026-11-15T08:00:00Z","updated_at":"2026-11-15T08:00:00Z","user_authored":true}],"has_more":false}`)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- New(reader).Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_tasks", Arguments: map[string]any{}})
	if err != nil || result.IsError || result.StructuredContent == nil || reader.calls != 1 || reader.method != localapi.RPCListTasks || string(reader.params) != `{"state":"open","limit":5}` {
		t.Fatalf("list_tasks result=%+v calls=%d method=%q params=%s err=%v", result, reader.calls, reader.method, reader.params, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Items []struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			UserAuthored bool   `json:"user_authored"`
		} `json:"items"`
	}
	if json.Unmarshal(encoded, &output) != nil || len(output.Items) != 1 || output.Items[0].ID != taskID || output.Items[0].Title != "Buy eggs" || !output.Items[0].UserAuthored {
		t.Fatalf("task output = %s", encoded)
	}
	invalid, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_tasks", Arguments: map[string]any{"state": "open", "unexpected": true}})
	if err != nil || !invalid.IsError || reader.calls != 1 {
		t.Fatalf("invalid task input result=%+v calls=%d err=%v", invalid, reader.calls, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("MCP server exit = %v", err)
		}
	case <-time.After(time.Second):
		t.Error("MCP server did not stop")
	}
}

func TestMCPGetWikiPageReturnsPagedDetailsAndValidatesID(t *testing.T) {
	const subjectID = "project-northstar"
	reader := &staticRPCReader{result: json.RawMessage(`{"found":true,"entity":{"id":"` + subjectID + `","kind":"project","name":"Northstar","aliases":["North Star"],"state":"confirmed"},"details":[{"type":"fact","id":"fact-two","text":"Mira owns rollback decisions.","state":"confirmed","sources":[{"user_authored":true}]}],"offset":1,"next_offset":2,"has_more":true}`)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- New(reader).Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_wiki_page", Arguments: map[string]any{"id": subjectID, "offset": 1, "limit": 1}})
	if err != nil || result.IsError || result.StructuredContent == nil || reader.calls != 1 || reader.method != localapi.RPCGetWikiPage || string(reader.params) != `{"id":"project-northstar","offset":1,"limit":1}` {
		t.Fatalf("get_wiki_page result=%+v calls=%d method=%q params=%s err=%v", result, reader.calls, reader.method, reader.params, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Details []struct {
			Text    string       `json:"text"`
			Sources []wikiSource `json:"sources"`
		} `json:"details"`
		NextOffset int  `json:"next_offset"`
		HasMore    bool `json:"has_more"`
	}
	if json.Unmarshal(encoded, &output) != nil || len(output.Details) != 1 || output.Details[0].Text != "Mira owns rollback decisions." || len(output.Details[0].Sources) != 1 || !output.Details[0].Sources[0].UserAuthored || output.NextOffset != 2 || !output.HasMore {
		t.Fatalf("Library page output = %s", encoded)
	}
	invalid, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_wiki_page", Arguments: map[string]any{"id": "bad id"}})
	if err != nil || !invalid.IsError || reader.calls != 1 {
		t.Fatalf("invalid Library page input result=%+v calls=%d err=%v", invalid, reader.calls, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("MCP server exit = %v", err)
		}
	case <-time.After(time.Second):
		t.Error("MCP server did not stop")
	}
}

func waitForRPC(t *testing.T, transport localapi.LocalTransport, endpoint localapi.EndpointDescriptor) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		conn, err := transport.Dial(ctx, endpoint)
		cancel()
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("local RPC listener did not become available")
}
