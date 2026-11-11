//go:build darwin

package client

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hazcod/blur/internal/browsercapture"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/ingest"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/storage"
)

const safariGrantLifetime = 24 * time.Hour
const safariGrantSecretName = "capture"

// safariCleanupTimeout bounds Keychain cleanup that runs after the caller's
// context may already be canceled.
const safariCleanupTimeout = 5 * time.Second

func deleteSafariGrantSecret(parent context.Context, secrets interface {
	Delete(context.Context, string) error
}) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), safariCleanupTimeout)
	defer cancel()
	return secrets.Delete(ctx, safariGrantSecretName)
}

const safariGrantService = "sh.blur.safari"

type safariGrantRecord struct {
	ID         string    `json:"id"`
	ClientID   string    `json:"client_id"`
	Credential []byte    `json:"credential"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type safariCaptureBridge struct {
	mu      sync.Mutex
	store   storage.EncryptedStore
	secrets interface {
		Put(context.Context, string, []byte) error
		Delete(context.Context, string) error
	}
	groupID, accessGroup string
	grants               *localapi.GrantStore
	grantID              string
	server               *browsercapture.UnixServer
	enabled              atomic.Bool
}

func newSafariCaptureBridge(store storage.EncryptedStore) *safariCaptureBridge {
	if store == nil || darwin.SafariAppGroupID == "" || darwin.SafariKeychainAccessGroup == "" {
		return nil
	}
	return &safariCaptureBridge{store: store, groupID: darwin.SafariAppGroupID, accessGroup: darwin.SafariKeychainAccessGroup}
}

func (b *safariCaptureBridge) Start(ctx context.Context) error {
	if b == nil || ctx == nil || ctx.Err() != nil {
		return localapi.ErrCanceled
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.server != nil {
		return localapi.ErrEndpointInUse
	}
	container, err := darwin.SafariContainerPath(b.groupID)
	if err != nil {
		return localapi.ErrEndpointUnavailable
	}
	path, err := darwin.SafariSocketPath(container)
	if err != nil {
		return localapi.ErrEndpointUnavailable
	}
	secrets, err := darwin.NewSharedSecretStore(safariGrantService, b.accessGroup)
	if err != nil {
		return localapi.ErrEndpointUnavailable
	}
	if err := secrets.Delete(ctx, safariGrantSecretName); err != nil {
		var state *platform.SecretStoreError
		if !errors.As(err, &state) || state.State != platform.SecretNotFound {
			return localapi.ErrEndpointUnavailable
		}
	}
	grants, err := localapi.NewGrantStore(time.Now, nil, 10000)
	if err != nil {
		return localapi.ErrEndpointUnavailable
	}
	grant, err := grants.Issue("safari-extension", []localapi.Scope{localapi.ScopeCaptureIngest}, safariGrantLifetime)
	if err != nil {
		return localapi.ErrEndpointUnavailable
	}
	record := safariGrantRecord{ID: grant.ID, ClientID: grant.ClientID, Credential: append([]byte(nil), grant.Credential...), ExpiresAt: grant.ExpiresAt}
	encoded, err := json.Marshal(record)
	clear(record.Credential)
	if err != nil {
		_ = grants.Revoke(grant.ID)
		clear(grant.Credential)
		return localapi.ErrEndpointUnavailable
	}
	if err := secrets.Put(ctx, safariGrantSecretName, encoded); err != nil {
		clear(encoded)
		_ = grants.Revoke(grant.ID)
		clear(grant.Credential)
		return localapi.ErrEndpointUnavailable
	}
	clear(encoded)
	clear(grant.Credential)
	b.enabled.Store(true)
	receiver, err := browsercapture.NewReceiver(grants, b.store, b.enabled.Load, time.Now)
	if err != nil {
		b.enabled.Store(false)
		_ = grants.Revoke(grant.ID)
		_ = deleteSafariGrantSecret(ctx, secrets)
		return localapi.ErrEndpointUnavailable
	}
	server, err := browsercapture.NewUnixServer(localapi.NewUnixTransport(browsercapture.MaxWireBytes), localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: path}, receiver)
	if err == nil {
		err = server.Start(ctx)
	}
	if err != nil {
		b.enabled.Store(false)
		receiver.Pause()
		if server != nil {
			_ = server.Stop()
		}
		_ = grants.Revoke(grant.ID)
		_ = deleteSafariGrantSecret(ctx, secrets)
		return localapi.ErrEndpointUnavailable
	}
	b.grants, b.grantID, b.server, b.secrets = grants, grant.ID, server, secrets
	return nil
}

func (b *safariCaptureBridge) Stop(ctx context.Context) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.enabled.Store(false)
	var err error
	if b.server != nil {
		err = b.server.Stop()
		b.server = nil
	}
	if b.grants != nil && b.grantID != "" {
		_ = b.grants.Revoke(b.grantID)
		b.grants = nil
		b.grantID = ""
	}
	if b.secrets != nil {
		if deleteErr := deleteSafariGrantSecret(ctx, b.secrets); deleteErr != nil {
			// Never mask a server Stop failure: join both so each is reported.
			slog.Warn("Safari capture credential could not be removed from Keychain", "result", "delete_failed")
			err = errors.Join(err, localapi.ErrEndpointUnavailable)
		}
		b.secrets = nil
	}
	return err
}

// CaptureController connects explicit user start/pause to the encrypted
// store. Capture does not run until Start; the adapter only asks macOS to show
// native permission guidance when startup detects missing Accessibility access.
func (a *LocalApp) CaptureController(source *darwin.Adapter) *ingest.Controller {
	if a == nil || a.store == nil || source == nil {
		return nil
	}
	return ingest.NewController(source, func(ctx context.Context) (func(context.Context) error, error) {
		config, err := a.Client.ApplicationPreferences(ctx)
		if err != nil {
			return nil, err
		}
		pipeline := capture.NewDarwinPipeline(source, capture.DefaultLimits(), captureExclusionsFromPreferences(config), nil)
		service, err := ingest.NewWithActivityAndObservationNotify(pipeline, a.store, a.activity, a.store, a.activityEpoch, slog.Default(), a.Client.NotifyCaptureChanged, a.notifyChronicleEvidence)
		if err != nil {
			return nil, err
		}
		service.SetCaptureCycleNotify(a.Client.NotifyCaptureStateChanged)
		return func(ctx context.Context) error {
			if shouldRunSafariPageCapture(config.CaptureEnabled, config.SafariPageCaptureEnabled, safariPageCaptureBuildConfigured()) {
				setSafariStatus(a.Client, safariCaptureStartingStatus)
				bridge := newSafariCaptureBridge(a.store)
				if err := bridge.Start(ctx); err != nil {
					slog.Warn("Safari page capture could not start; native app capture remains active")
					setSafariStatus(a.Client, safariCaptureUnavailableStatus)
				} else {
					setSafariStatus(a.Client, safariCaptureReadyStatus)
					defer func() {
						stopCtx, cancel := context.WithTimeout(context.Background(), safariCleanupTimeout)
						defer cancel()
						if err := bridge.Stop(stopCtx); err != nil {
							slog.Warn("Safari page capture did not stop cleanly; a capture credential may remain in Keychain", "result", "stop_failed")
						}
						setSafariStatus(a.Client, safariCaptureOffStatus)
					}()
				}
			} else {
				setSafariStatus(a.Client, safariCaptureOffStatus)
			}
			return service.Run(ctx)
		}, nil
	})
}

func setSafariStatus(client *Client, status string) {
	if client != nil {
		client.setSafariCaptureStatus(status)
	}
}
