//go:build darwin || linux

package localapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRPCUsesPrivateUnixSocketAndRejectsForeignGrant(t *testing.T) {
	dir, err := os.MkdirTemp(os.TempDir(), "b-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	endpoint := EndpointDescriptor{Kind: EndpointUnix, Address: filepath.Join(dir, "mcp.sock")}
	transport := NewUnixTransport(maxRPCFrameBytes)
	probe, err := transport.Listen(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("create private Unix listener: %v (path bytes=%d)", err, len(endpoint.Address))
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	serverGrants, err := NewGrantStore(nil, nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := serverGrants.Issue("claude.mcp", []Scope{ScopeMCPBoundedRead}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeRPC(ctx, transport, endpoint, serverGrants, func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
			called = true
			if method != RPCSearchMemory || string(params) != `{"query":"launch"}` {
				t.Errorf("handler received method=%q params=%s", method, params)
			}
			return json.RawMessage(`{"count":1}`), nil
		})
	}()
	waitForRPCListener(t, transport, endpoint)
	info, err := os.Lstat(endpoint.Address)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, want owner-only socket 0600", info.Mode())
	}
	result, err := NewRPCClient(transport, endpoint, grant).Call(context.Background(), RPCSearchMemory, json.RawMessage(`{"query":"launch"}`))
	if err != nil || string(result) != `{"count":1}` || !called {
		t.Fatalf("authorized call result=%s called=%t err=%v", result, called, err)
	}
	foreignStore, err := NewGrantStore(nil, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	foreignGrant, err := foreignStore.Issue("claude.mcp", []Scope{ScopeMCPBoundedRead}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	called = false
	if _, err := NewRPCClient(transport, endpoint, foreignGrant).Call(context.Background(), RPCSearchMemory, json.RawMessage(`{"query":"launch"}`)); err != ErrRPCUnauthorized {
		t.Fatalf("foreign grant error = %v, want ErrRPCUnauthorized", err)
	}
	if called {
		t.Fatal("read handler ran for a grant unknown to the server")
	}
	cancel()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("ServeRPC after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ServeRPC did not stop after cancellation")
	}
	if _, err := os.Lstat(endpoint.Address); !os.IsNotExist(err) {
		t.Fatalf("socket remains after server close: %v", err)
	}
}
