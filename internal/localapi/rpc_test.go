package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRPCClientAndServerAuthenticateBoundedRead(t *testing.T) {
	transport := NewMemoryTransport(8, 32<<10)
	endpoint := EndpointDescriptor{Kind: EndpointUnix, Address: "/tmp/blur-rpc-test"}
	grants, err := NewGrantStore(nil, nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.Issue("claude.mcp", []Scope{ScopeMCPBoundedRead}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler := RPCReadHandler(func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		called = true
		if method != "search_memory" || string(params) != `{"query":"launch"}` {
			t.Fatalf("handler got method=%q params=%s", method, params)
		}
		return json.RawMessage(`{"results":[{"text":"approved"}]}`), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() { serverDone <- ServeRPC(ctx, transport, endpoint, grants, handler) }()
	waitForRPCListener(t, transport, endpoint)

	client := NewRPCClient(transport, endpoint, grant)
	result, err := client.Call(context.Background(), "search_memory", json.RawMessage(`{"query":"launch"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !called || string(result) != `{"results":[{"text":"approved"}]}` {
		t.Fatalf("called=%t result=%s", called, result)
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
}

func TestRPCRejectsCapturedRequestAfterOwnerRestart(t *testing.T) {
	base := NewMemoryTransport(8, 32<<10)
	transport := &recordingRPCTransport{LocalTransport: base}
	endpoint := EndpointDescriptor{Kind: EndpointUnix, Address: "/tmp/blur-rpc-restart-replay"}
	var calls atomic.Int32
	handler := RPCReadHandler(func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"results":[]}`), nil
	})
	first := NewRPCOwner(transport, endpoint, handler)
	grant, err := first.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	client := NewRPCClient(transport, endpoint, grant)
	if _, err := client.Call(context.Background(), RPCSearchMemory, json.RawMessage(`{"query":"alpha"}`)); err != nil {
		t.Fatal(err)
	}
	staleFrame := transport.lastSent()
	client.Close()
	if err := first.Stop(); err != nil {
		t.Fatal(err)
	}
	second := NewRPCOwner(transport, endpoint, handler)
	if err := second.StartWithGrant(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = second.Stop()
		clear(grant.Credential)
		clear(staleFrame)
	}()
	if _, err := NewRPCClient(transport, endpoint, grant).Call(context.Background(), RPCSearchMemory, json.RawMessage(`{"query":"alpha"}`)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("valid requests handled = %d, want two", calls.Load())
	}

	conn, err := base.Dial(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	challenge, err := conn.Receive(ctx)
	if err != nil || len(challenge) != rpcChallengeBytes {
		clear(challenge)
		t.Fatalf("receive fresh challenge: len=%d err=%v", len(challenge), err)
	}
	clear(challenge)
	if err := conn.Send(context.Background(), staleFrame); err != nil {
		t.Fatal(err)
	}
	var response rpcResponse
	frame, err := conn.Receive(ctx)
	if err != nil {
		t.Fatalf("receive replay response: %v", err)
	}
	defer clear(frame)
	if decodeRPCJSON(frame, &response) != nil {
		t.Fatal("replay response was not valid protocol JSON")
	}
	if response.Error != "unauthorized" || calls.Load() != 2 {
		t.Fatalf("captured request after restart response=%+v handler calls=%d", response, calls.Load())
	}
}

type recordingRPCTransport struct {
	LocalTransport
	mu   sync.Mutex
	sent []byte
}

func (t *recordingRPCTransport) Dial(ctx context.Context, endpoint EndpointDescriptor) (LocalConn, error) {
	conn, err := t.LocalTransport.Dial(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return &recordingRPCConn{LocalConn: conn, owner: t}, nil
}

func (t *recordingRPCTransport) lastSent() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.sent...)
}

type recordingRPCConn struct {
	LocalConn
	owner *recordingRPCTransport
}

func (c *recordingRPCConn) Send(ctx context.Context, frame []byte) error {
	if err := c.LocalConn.Send(ctx, frame); err != nil {
		return err
	}
	c.owner.mu.Lock()
	clear(c.owner.sent)
	c.owner.sent = append(c.owner.sent[:0], frame...)
	c.owner.mu.Unlock()
	return nil
}

func TestRPCServerRejectsUnauthorizedGrantBeforeCallingHandler(t *testing.T) {
	transport := NewMemoryTransport(8, 32<<10)
	endpoint := EndpointDescriptor{Kind: EndpointUnix, Address: "/tmp/blur-rpc-unauthorized"}
	serverGrants, err := NewGrantStore(nil, nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	clientGrants, err := NewGrantStore(nil, nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := clientGrants.Issue("claude.mcp", []Scope{ScopeMCPBoundedRead}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeRPC(ctx, transport, endpoint, serverGrants, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			called = true
			return json.RawMessage(`{}`), nil
		})
	}()
	waitForRPCListener(t, transport, endpoint)
	_, err = NewRPCClient(transport, endpoint, grant).Call(context.Background(), "search_memory", json.RawMessage(`{}`))
	if !errors.Is(err, ErrRPCUnauthorized) {
		t.Fatalf("unauthorized call error = %v, want ErrRPCUnauthorized", err)
	}
	if called {
		t.Fatal("handler ran before request authorization")
	}
	cancel()
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("ServeRPC did not stop after cancellation")
	}
}

func waitForRPCListener(t *testing.T, transport LocalTransport, endpoint EndpointDescriptor) {
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
	t.Fatal("RPC listener did not become available")
}

func TestRPCClientRejectsUnboundedOrMalformedRequestBeforeDial(t *testing.T) {
	transport := NewMemoryTransport(1, 32<<10)
	grants, err := NewGrantStore(nil, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.Issue("claude.mcp", []Scope{ScopeMCPBoundedRead}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	client := NewRPCClient(transport, EndpointDescriptor{Kind: EndpointUnix, Address: "/tmp/blur-rpc-invalid"}, grant)
	for _, test := range []struct {
		name   string
		method string
		params json.RawMessage
	}{
		{name: "empty method", params: json.RawMessage(`{}`)},
		{name: "mutation method", method: "delete_all_memory", params: json.RawMessage(`{}`)},
		{name: "malformed params", method: "search_memory", params: json.RawMessage(`{"query":`)},
		{name: "oversized params", method: "search_memory", params: json.RawMessage(`{"query":"` + strings.Repeat("x", 33<<10) + `"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.Call(context.Background(), test.method, test.params); !errors.Is(err, ErrRPCInvalid) {
				t.Fatalf("Call error = %v, want ErrRPCInvalid", err)
			}
		})
	}
}
