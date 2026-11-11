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

func TestMCPExposesOnlyBoundedReadOnlyMemorySearch(t *testing.T) {
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
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "search_memory" {
		t.Fatalf("MCP tools = %+v, want only search_memory", tools.Tools)
	}
	if tools.Tools[0].Annotations == nil || !tools.Tools[0].Annotations.ReadOnlyHint || !tools.Tools[0].Annotations.IdempotentHint {
		t.Fatalf("search_memory annotations = %+v, want read-only and idempotent", tools.Tools[0].Annotations)
	}
	schema, ok := tools.Tools[0].InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("search_memory input schema type = %T", tools.Tools[0].InputSchema)
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
