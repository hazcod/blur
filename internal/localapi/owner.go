package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

const mcpGrantLifetime = 365 * 24 * time.Hour

var ErrRPCAlreadyRunning = errors.New("local RPC owner already running")

// RPCOwner owns a single read-only Claude grant and the server that accepts
// it. The issued credential is returned once to the caller for Keychain
// storage; the owner retains only its revocable grant ID.
type RPCOwner struct {
	mu        sync.Mutex
	transport LocalTransport
	endpoint  EndpointDescriptor
	handler   RPCReadHandler
	grants    *GrantStore
	grantID   string
	cancel    context.CancelFunc
	done      chan error
}

func NewRPCOwner(transport LocalTransport, endpoint EndpointDescriptor, handler RPCReadHandler) *RPCOwner {
	return &RPCOwner{transport: transport, endpoint: endpoint, handler: handler}
}

// Start binds the local endpoint, waits until it can accept connections, and
// issues exactly one mcp:bounded-read grant to claude.mcp.
func (o *RPCOwner) Start(ctx context.Context) (IssuedGrant, error) {
	if o == nil || ctx == nil || ctx.Err() != nil || o.transport == nil || o.endpoint.Validate() != nil || o.handler == nil {
		return IssuedGrant{}, ErrRPCInvalid
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cancel != nil {
		return IssuedGrant{}, ErrRPCAlreadyRunning
	}
	grants, err := NewGrantStore(nil, nil, 4096)
	if err != nil {
		return IssuedGrant{}, ErrRPCUnavailable
	}
	grant, err := grants.Issue("claude.mcp", []Scope{ScopeMCPBoundedRead}, mcpGrantLifetime)
	if err != nil {
		return IssuedGrant{}, ErrRPCUnavailable
	}
	if err := o.startLocked(ctx, grants, grant); err != nil {
		clear(grant.Credential)
		return IssuedGrant{}, err
	}
	return grant, nil
}

// StartWithGrant restores a credential protected by the OS secret store, so
// already-running MCP clients remain authorized across a Blur restart.
func (o *RPCOwner) StartWithGrant(ctx context.Context, grant IssuedGrant) error {
	if o == nil || ctx == nil || ctx.Err() != nil || o.transport == nil || o.endpoint.Validate() != nil || o.handler == nil || grant.ClientID != "claude.mcp" || len(grant.Scopes) != 1 || grant.Scopes[0] != ScopeMCPBoundedRead {
		return ErrRPCInvalid
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cancel != nil {
		return ErrRPCAlreadyRunning
	}
	grants, err := NewGrantStore(nil, nil, 4096)
	if err != nil || grants.Restore(grant) != nil {
		return ErrRPCUnauthorized
	}
	return o.startLocked(ctx, grants, grant)
}

func (o *RPCOwner) startLocked(ctx context.Context, grants *GrantStore, grant IssuedGrant) error {
	serverCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	o.grants, o.grantID, o.cancel, o.done = grants, grant.ID, cancel, done
	go func() { done <- ServeRPC(serverCtx, o.transport, o.endpoint, grants, o.handler) }()
	if err := waitRPCReady(ctx, o.transport, o.endpoint, done); err != nil {
		cancel()
		_ = grants.Revoke(grant.ID)
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		o.grants, o.grantID, o.cancel, o.done = nil, "", nil, nil
		return err
	}
	return nil
}

// Stop revokes the grant before joining the server. It is safe to call more
// than once and removes the endpoint through the transport's Close path.
func (o *RPCOwner) Stop() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	cancel, done, grants, id := o.cancel, o.done, o.grants, o.grantID
	o.cancel, o.done, o.grants, o.grantID = nil, nil, nil, ""
	o.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	if grants != nil && id != "" {
		_ = grants.Revoke(id)
	}
	select {
	case err := <-done:
		if err != nil {
			return ErrRPCUnavailable
		}
	case <-time.After(3 * time.Second):
		return ErrRPCUnavailable
	}
	return nil
}

func waitRPCReady(ctx context.Context, transport LocalTransport, endpoint EndpointDescriptor, done <-chan error) error {
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		probeCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		conn, err := transport.Dial(probeCtx, endpoint)
		cancel()
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case err := <-done:
			if err != nil {
				return ErrRPCUnavailable
			}
			return ErrRPCUnavailable
		case <-deadline.C:
			return ErrRPCUnavailable
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// EncodeIssuedGrant encodes the explicit Keychain record format used by
// clients. IssuedGrant's default JSON marshaler intentionally hides secrets.
func EncodeIssuedGrant(grant IssuedGrant) ([]byte, error) {
	if !validGrantID(grant.ID) || grant.ClientID != "claude.mcp" || len(grant.Scopes) != 1 || grant.Scopes[0] != ScopeMCPBoundedRead || len(grant.Credential) != 32 || allZero(grant.Credential) || grant.IssuedAt.IsZero() || !grant.ExpiresAt.After(grant.IssuedAt) || grant.ExpiresAt.Sub(grant.IssuedAt) > MaxGrantLifetime {
		return nil, ErrRPCInvalid
	}
	record := struct {
		ID         string    `json:"id"`
		ClientID   string    `json:"client_id"`
		Scopes     []Scope   `json:"scopes"`
		IssuedAt   time.Time `json:"issued_at"`
		ExpiresAt  time.Time `json:"expires_at"`
		Credential []byte    `json:"credential"`
	}{grant.ID, grant.ClientID, append([]Scope(nil), grant.Scopes...), grant.IssuedAt, grant.ExpiresAt, append([]byte(nil), grant.Credential...)}
	defer clear(record.Credential)
	return json.Marshal(record)
}

// DecodeIssuedGrant decodes the explicit Keychain record format used by
// clients. IssuedGrant's default JSON marshaler intentionally hides secrets.
func DecodeIssuedGrant(data []byte) (IssuedGrant, error) {
	if len(data) == 0 || len(data) > 2048 || !json.Valid(data) {
		return IssuedGrant{}, ErrRPCInvalid
	}
	var record struct {
		ID         string    `json:"id"`
		ClientID   string    `json:"client_id"`
		Scopes     []Scope   `json:"scopes"`
		IssuedAt   time.Time `json:"issued_at"`
		ExpiresAt  time.Time `json:"expires_at"`
		Credential []byte    `json:"credential"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || !validGrantID(record.ID) || record.ClientID != "claude.mcp" || len(record.Scopes) != 1 || record.Scopes[0] != ScopeMCPBoundedRead || len(record.Credential) != 32 || allZero(record.Credential) || record.IssuedAt.IsZero() || !record.ExpiresAt.After(record.IssuedAt) || record.ExpiresAt.Sub(record.IssuedAt) > MaxGrantLifetime {
		clear(record.Credential)
		return IssuedGrant{}, ErrRPCInvalid
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		clear(record.Credential)
		return IssuedGrant{}, ErrRPCInvalid
	}
	return IssuedGrant{ID: record.ID, ClientID: record.ClientID, Scopes: record.Scopes, IssuedAt: record.IssuedAt, ExpiresAt: record.ExpiresAt, Credential: record.Credential}, nil
}
