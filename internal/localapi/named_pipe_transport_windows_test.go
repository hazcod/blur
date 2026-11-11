//go:build windows

package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestNamedPipeRPCUsesSameUserPipeAndGrantProof(t *testing.T) {
	endpoint, err := CurrentUserNamedPipeEndpoint()
	if err != nil || endpoint.Validate() != nil {
		t.Fatalf("current-user endpoint = %+v, %v", endpoint, err)
	}
	transport := NewNamedPipeTransport(maxRPCFrameBytes)
	grants, err := NewGrantStore(nil, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.Issue("claude.mcp", []Scope{ScopeMCPBoundedRead}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var called atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeRPC(ctx, transport, endpoint, grants, func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
			called.Store(true)
			if method != RPCSearchMemory || string(params) != `{"query":"launch"}` {
				t.Errorf("handler received method=%q params=%s", method, params)
			}
			return json.RawMessage(`{"count":1}`), nil
		})
	}()
	waitForNamedPipe(t, transport, endpoint)
	result, err := NewRPCClient(transport, endpoint, grant).Call(context.Background(), RPCSearchMemory, json.RawMessage(`{"query":"launch"}`))
	if err != nil || string(result) != `{"count":1}` || !called.Load() {
		t.Fatalf("authorized result=%s called=%t err=%v", result, called.Load(), err)
	}
	foreignStore, err := NewGrantStore(nil, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	foreignGrant, err := foreignStore.Issue("claude.mcp", []Scope{ScopeMCPBoundedRead}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	called.Store(false)
	if _, err := NewRPCClient(transport, endpoint, foreignGrant).Call(context.Background(), RPCSearchMemory, json.RawMessage(`{"query":"launch"}`)); err != ErrRPCUnauthorized {
		t.Fatalf("foreign grant error = %v, want ErrRPCUnauthorized", err)
	}
	if called.Load() {
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
	listener, err := transport.Listen(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("named pipe remained after server shutdown: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNamedPipeAcceptCancellationClosesPendingListener(t *testing.T) {
	endpoint, err := CurrentUserNamedPipeEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	transport := NewNamedPipeTransport(maxRPCFrameBytes)
	listener, err := transport.Listen(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := listener.Accept(ctx); result <- err }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, ErrCanceled) {
			t.Fatalf("Accept cancellation = %v, want ErrCanceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Accept did not stop on cancellation")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNamedPipeTransportRejectsAnotherAccountEndpoint(t *testing.T) {
	endpoint, err := CurrentUserNamedPipeEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Address += "-other"
	transport := NewNamedPipeTransport(maxRPCFrameBytes)
	if _, err := transport.Listen(context.Background(), endpoint); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("Listen for a non-current-user endpoint = %v, want ErrInvalidEndpoint", err)
	}
	if _, err := transport.Dial(context.Background(), endpoint); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("Dial for a non-current-user endpoint = %v, want ErrInvalidEndpoint", err)
	}
}

func waitForNamedPipe(t *testing.T, transport *NamedPipeTransport, endpoint EndpointDescriptor) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		conn, err := transport.Dial(ctx, endpoint)
		if err == nil {
			_ = conn.Close()
			return
		}
		if ctx.Err() != nil {
			t.Fatalf("named pipe did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
