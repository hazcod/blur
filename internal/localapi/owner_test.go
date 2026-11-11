package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRPCOwnerStartsReadOnlyEndpointAndRevokesOnStop(t *testing.T) {
	transport := NewMemoryTransport(4, 32<<10)
	endpoint := EndpointDescriptor{Kind: EndpointUnix, Address: "/tmp/blur-rpc-owner"}
	owner := NewRPCOwner(transport, endpoint, func(_ context.Context, method string, _ json.RawMessage) (json.RawMessage, error) {
		if method != RPCSearchMemory {
			return nil, ErrRPCInvalid
		}
		return json.RawMessage(`{"results":[],"truncated":false}`), nil
	})
	grant, err := owner.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if grant.ClientID != "claude.mcp" || len(grant.Scopes) != 1 || grant.Scopes[0] != ScopeMCPBoundedRead || len(grant.Credential) != 32 {
		t.Fatalf("owner issued broader-than-read grant: %s", grant)
	}
	client := NewRPCClient(transport, endpoint, grant)
	defer client.Close()
	clear(grant.Credential)
	if _, err := client.Call(context.Background(), RPCSearchMemory, json.RawMessage(`{"query":"standup"}`)); err != nil {
		t.Fatalf("ready owner rejected its read-only grant: %v", err)
	}
	if err := owner.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), RPCSearchMemory, json.RawMessage(`{"query":"standup"}`)); !errors.Is(err, ErrRPCUnavailable) {
		t.Fatalf("RPC remained available after owner stop: %v", err)
	}
	if err := owner.Stop(); err != nil {
		t.Fatalf("second Stop() was not idempotent: %v", err)
	}
}

func TestRPCOwnerRejectsDuplicateStart(t *testing.T) {
	transport := NewMemoryTransport(4, 32<<10)
	owner := NewRPCOwner(transport, EndpointDescriptor{Kind: EndpointUnix, Address: "/tmp/blur-rpc-owner-duplicate"}, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	if _, err := owner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer owner.Stop()
	if _, err := owner.Start(context.Background()); err == nil {
		t.Fatal("duplicate owner start succeeded")
	}
}

func TestIssuedGrantHasAnExplicitSecretEncodingForKeychainOnly(t *testing.T) {
	grants, err := NewGrantStore(nil, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.Issue("claude.mcp", []Scope{ScopeMCPBoundedRead}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(grant.Credential)
	redacted, err := json.Marshal(grant)
	if err != nil || string(redacted) != `{}` {
		t.Fatalf("ordinary grant JSON exposed fields: %s (err=%v)", redacted, err)
	}
	encoded, err := EncodeIssuedGrant(grant)
	if err != nil {
		t.Fatal(err)
	}
	trailing := append(append([]byte(nil), encoded...), []byte(" {}")...)
	decoded, err := DecodeIssuedGrant(encoded)
	clear(encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(decoded.Credential)
	if decoded.ID != grant.ID || decoded.ClientID != grant.ClientID || len(decoded.Scopes) != 1 || decoded.Scopes[0] != ScopeMCPBoundedRead || !decoded.ExpiresAt.Equal(grant.ExpiresAt) || string(decoded.Credential) != string(grant.Credential) {
		t.Fatal("explicit Keychain grant encoding failed to round-trip")
	}
	if _, err := DecodeIssuedGrant(trailing); err == nil {
		t.Fatal("grant decoder accepted trailing JSON")
	}
	clear(trailing)
}

func TestRPCOwnerWaitsForCancellationAndCleansListener(t *testing.T) {
	transport := NewMemoryTransport(4, 32<<10)
	endpoint := EndpointDescriptor{Kind: EndpointUnix, Address: "/tmp/blur-rpc-owner-cancel"}
	owner := NewRPCOwner(transport, endpoint, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	if _, err := owner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- owner.Stop() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("owner did not join its RPC server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := transport.Dial(ctx, endpoint); err == nil {
		t.Fatal("RPC listener remained after owner stop")
	}
}

func TestRPCOwnerRestoresPersistedGrantAcrossAppRestart(t *testing.T) {
	transport := NewMemoryTransport(4, 32<<10)
	endpoint := EndpointDescriptor{Kind: EndpointUnix, Address: "/tmp/blur-rpc-owner-restart"}
	handler := func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"results":[],"truncated":false}`), nil
	}
	first := NewRPCOwner(transport, endpoint, handler)
	grant, err := first.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Stop(); err != nil {
		t.Fatal(err)
	}
	second := NewRPCOwner(transport, endpoint, handler)
	if err := second.StartWithGrant(context.Background(), grant); err != nil {
		t.Fatalf("restored owner rejected its persisted grant: %v", err)
	}
	defer second.Stop()
	if _, err := NewRPCClient(transport, endpoint, grant).Call(context.Background(), RPCSearchMemory, json.RawMessage(`{"query":"standup"}`)); err != nil {
		t.Fatalf("existing MCP client credential failed after app restart: %v", err)
	}
	clear(grant.Credential)
}

func TestGrantStoreRestoresOnlyLiveWellFormedCredentials(t *testing.T) {
	issuer, err := NewGrantStore(nil, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := issuer.Issue("claude.mcp", []Scope{ScopeMCPBoundedRead}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(grant.Credential)
	restored, err := NewGrantStore(nil, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(grant); err != nil {
		t.Fatalf("Restore() valid live credential: %v", err)
	}
	request := [32]byte{1}
	proof, err := NewProof(grant, ScopeMCPBoundedRead, request, time.Now(), nil)
	if err != nil || restored.Authorize(proof, request) != nil {
		t.Fatalf("restored credential did not authorize: proof=%v", err)
	}
	for name, bad := range map[string]IssuedGrant{
		"expired":         func() IssuedGrant { x := grant; x.ExpiresAt = time.Now().Add(-time.Minute); return x }(),
		"broad scope":     func() IssuedGrant { x := grant; x.Scopes = []Scope{ScopeMCPBoundedRead, ScopeCLIAdmin}; return x }(),
		"bad ID":          func() IssuedGrant { x := grant; x.ID = "short"; return x }(),
		"zero credential": func() IssuedGrant { x := grant; x.Credential = make([]byte, 32); return x }(),
	} {
		t.Run(name, func(t *testing.T) {
			target, _ := NewGrantStore(nil, nil, 8)
			if err := target.Restore(bad); err == nil {
				t.Fatal("Restore() accepted unsafe credential")
			}
		})
	}
}
