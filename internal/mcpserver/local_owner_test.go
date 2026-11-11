package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/platform"
)

type ownerTestSecrets struct {
	values   map[string][]byte
	getErr   error
	putErr   error
	getStart chan struct{}
	getBlock <-chan struct{}
}

func (s *ownerTestSecrets) Capability(context.Context) (platform.CapabilityReport, error) {
	return platform.CapabilityReport{ID: "secret_store", State: platform.Available}, nil
}
func (s *ownerTestSecrets) Get(_ context.Context, name string, max int) ([]byte, error) {
	if s.getStart != nil {
		select {
		case s.getStart <- struct{}{}:
		default:
		}
	}
	if s.getBlock != nil {
		<-s.getBlock
	}
	if s.getErr != nil {
		return nil, s.getErr
	}
	value, ok := s.values[name]
	if !ok {
		return nil, &platform.SecretStoreError{State: platform.SecretNotFound}
	}
	if len(value) > max {
		return nil, &platform.SecretStoreError{State: platform.SecretCorrupt}
	}
	return append([]byte(nil), value...), nil
}
func (s *ownerTestSecrets) Put(_ context.Context, name string, value []byte) error {
	if s.putErr != nil {
		return s.putErr
	}
	if s.values == nil {
		s.values = make(map[string][]byte)
	}
	s.values[name] = append([]byte(nil), value...)
	return nil
}
func (s *ownerTestSecrets) Delete(_ context.Context, name string) error {
	if _, ok := s.values[name]; !ok {
		return &platform.SecretStoreError{State: platform.SecretNotFound}
	}
	clear(s.values[name])
	delete(s.values, name)
	return nil
}

func TestLocalOwnerRestoresCredentialAcrossAppRestartAndRevokesOnDisable(t *testing.T) {
	ctx := context.Background()
	secrets := &ownerTestSecrets{}
	transport := localapi.NewMemoryTransport(8, 32<<10)
	endpoint := localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: "/tmp/blur-mcp-owner"}
	handler := func(_ context.Context, method string, _ json.RawMessage) (json.RawMessage, error) {
		if method != localapi.RPCSearchMemory {
			return nil, localapi.ErrRPCInvalid
		}
		return json.RawMessage(`{"results":[],"truncated":false}`), nil
	}
	first := NewLocalOwner(secrets, transport, endpoint, handler)
	if err := first.SetEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if !first.Enabled() {
		t.Fatal("owner did not report enabled")
	}
	raw, err := secrets.Get(ctx, GrantSecretName, 2048)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := localapi.DecodeIssuedGrant(raw)
	clear(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(grant.Credential)
	client := localapi.NewRPCClient(transport, endpoint, grant)
	if _, err := client.Call(ctx, localapi.RPCSearchMemory, json.RawMessage(`{"query":"standup"}`)); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := NewLocalOwner(secrets, transport, endpoint, handler)
	if err := second.SetEnabled(ctx, true); err != nil {
		t.Fatalf("restored owner failed: %v", err)
	}
	if _, err := client.Call(ctx, localapi.RPCSearchMemory, json.RawMessage(`{"query":"standup"}`)); err != nil {
		t.Fatalf("pre-existing MCP process credential failed after Blur restart: %v", err)
	}
	if err := second.SetEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	if second.Enabled() {
		t.Fatal("owner remained enabled after explicit disable")
	}
	if _, err := secrets.Get(ctx, GrantSecretName, 2048); err == nil {
		t.Fatal("explicit disable left the credential in secure storage")
	}
	if _, err := client.Call(ctx, localapi.RPCSearchMemory, json.RawMessage(`{"query":"standup"}`)); !errors.Is(err, localapi.ErrRPCUnavailable) {
		t.Fatalf("disabled owner still served RPC: %v", err)
	}
}

func TestLocalOwnerSecureStorageFailureRollsBackListener(t *testing.T) {
	secrets := &ownerTestSecrets{putErr: errors.New("keychain locked")}
	transport := localapi.NewMemoryTransport(4, 32<<10)
	endpoint := localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: "/tmp/blur-mcp-owner-fail"}
	owner := NewLocalOwner(secrets, transport, endpoint, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	if err := owner.SetEnabled(context.Background(), true); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("enable error = %v, want ErrOwnerUnavailable", err)
	}
	if owner.Enabled() {
		t.Fatal("owner reported enabled after keychain write failed")
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := transport.Dial(probeCtx, endpoint); err == nil {
		t.Fatal("listener stayed up without a protected client credential")
	}
}

func TestLocalOwnerDoesNotRotateOrExposeUnreadableGrant(t *testing.T) {
	secrets := &ownerTestSecrets{getErr: &platform.SecretStoreError{State: platform.SecretLocked}}
	owner := NewLocalOwner(secrets, localapi.NewMemoryTransport(2, 32<<10), localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: "/tmp/blur-mcp-owner-locked"}, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	if err := owner.SetEnabled(context.Background(), true); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("locked Keychain enable error = %v", err)
	}
	if owner.Enabled() {
		t.Fatal("owner enabled with an unreadable persisted grant")
	}
}

func TestLocalOwnerDisableDeletesCredentialEvenWhenListenerFailedToStart(t *testing.T) {
	secrets := &ownerTestSecrets{values: map[string][]byte{GrantSecretName: []byte("stale grant")}}
	owner := NewLocalOwner(secrets, localapi.NewMemoryTransport(2, 32<<10), localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: "/tmp/blur-mcp-owner-stale"}, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	if err := owner.SetEnabled(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, ok := secrets.values[GrantSecretName]; ok {
		t.Fatal("explicit disable left an inactive stale grant in secure storage")
	}
}

func TestLocalOwnerEnabledStatusDoesNotBlockOnKeychainPrompt(t *testing.T) {
	started := make(chan struct{}, 1)
	continueGet := make(chan struct{})
	secrets := &ownerTestSecrets{getStart: started, getBlock: continueGet}
	owner := NewLocalOwner(secrets, localapi.NewMemoryTransport(2, 32<<10), localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: "/tmp/blur-mcp-owner-nonblocking"}, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	setDone := make(chan error, 1)
	go func() { setDone <- owner.SetEnabled(context.Background(), true) }()
	<-started
	statusDone := make(chan bool, 1)
	go func() { statusDone <- owner.Enabled() }()
	select {
	case enabled := <-statusDone:
		if enabled {
			t.Fatal("bridge reported enabled before Keychain setup completed")
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("reading bridge status blocked behind Keychain access")
	}
	close(continueGet)
	if err := <-setDone; err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}
