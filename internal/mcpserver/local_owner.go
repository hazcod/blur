package mcpserver

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/platform"
)

const GrantSecretName = "claude-mcp-grant"

var ErrOwnerUnavailable = errors.New("Blur Claude bridge unavailable")

// LocalOwner couples the owner-side RPC listener to an OS-protected grant.
// Stopping Blur closes the listener but preserves the credential so the same
// Claude subprocess remains authorized after Blur restarts.
type LocalOwner struct {
	mu      sync.Mutex
	secrets platform.SecretStore
	rpc     *localapi.RPCOwner
	enabled atomic.Bool
}

func NewLocalOwner(secrets platform.SecretStore, transport localapi.LocalTransport, endpoint localapi.EndpointDescriptor, handler localapi.RPCReadHandler) *LocalOwner {
	return &LocalOwner{secrets: secrets, rpc: localapi.NewRPCOwner(transport, endpoint, handler)}
}

func (o *LocalOwner) Enabled() bool {
	if o == nil {
		return false
	}
	return o.enabled.Load()
}

// SetEnabled is opt-in: it does not access Keychain unless the user enables or
// disables the Claude bridge in Blur's privacy settings.
func (o *LocalOwner) SetEnabled(ctx context.Context, enabled bool) error {
	if o == nil || o.secrets == nil || o.rpc == nil || ctx == nil || ctx.Err() != nil {
		return ErrOwnerUnavailable
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if enabled && o.enabled.Load() {
		return nil
	}
	if !enabled {
		var stopErr error
		if o.enabled.Load() {
			stopErr = o.rpc.Stop()
		}
		o.enabled.Store(false)
		deleteErr := o.deleteGrant(ctx)
		if stopErr != nil || deleteErr != nil {
			return ErrOwnerUnavailable
		}
		return nil
	}

	record, err := o.secrets.Get(ctx, GrantSecretName, 2048)
	if err == nil {
		grant, decodeErr := localapi.DecodeIssuedGrant(record)
		clear(record)
		if decodeErr != nil {
			return ErrOwnerUnavailable
		}
		if time.Now().Before(grant.ExpiresAt) {
			startErr := o.rpc.StartWithGrant(ctx, grant)
			clear(grant.Credential)
			if startErr != nil {
				return ErrOwnerUnavailable
			}
			o.enabled.Store(true)
			return nil
		}
		clear(grant.Credential)
		if err := o.deleteGrant(ctx); err != nil {
			return ErrOwnerUnavailable
		}
	} else if !missingGrant(err) {
		return ErrOwnerUnavailable
	}

	grant, err := o.rpc.Start(ctx)
	if err != nil {
		return ErrOwnerUnavailable
	}
	defer clear(grant.Credential)
	encoded, err := localapi.EncodeIssuedGrant(grant)
	if err != nil {
		_ = o.rpc.Stop()
		return ErrOwnerUnavailable
	}
	putErr := o.secrets.Put(ctx, GrantSecretName, encoded)
	clear(encoded)
	if putErr != nil {
		_ = o.rpc.Stop()
		return ErrOwnerUnavailable
	}
	o.enabled.Store(true)
	return nil
}

// Close stops the server at app shutdown but preserves Keychain state so the
// next process can restore the same grant. Explicit disable deletes it.
func (o *LocalOwner) Close() error {
	if o == nil || o.rpc == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	err := o.rpc.Stop()
	o.enabled.Store(false)
	if err != nil {
		return ErrOwnerUnavailable
	}
	return nil
}

func (o *LocalOwner) deleteGrant(ctx context.Context) error {
	err := o.secrets.Delete(ctx, GrantSecretName)
	if missingGrant(err) {
		return nil
	}
	return err
}

func missingGrant(err error) bool {
	var secretErr *platform.SecretStoreError
	return errors.As(err, &secretErr) && secretErr != nil && secretErr.State == platform.SecretNotFound
}
