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

	"github.com/hazcod/blur/internal/acceptance"
	"github.com/hazcod/blur/internal/browsercapture"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/ingest"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/preferences"
	"github.com/hazcod/blur/internal/storage"
)

const safariGrantLifetime = 24 * time.Hour
const safariGrantSecretName = "capture"

const (
	safariGrantRenewBefore         = time.Hour
	safariGrantOverlap             = 2 * time.Minute
	safariGrantRenewInterval       = time.Minute
	safariGrantRenewalFailedStatus = "Safari page capture authorization could not be renewed. Blur will retry; native capture remains active."
	safariGrantExpiredStatus       = "Safari page capture authorization expired. Blur is retrying; native capture remains active."
)

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
	mu          sync.Mutex
	store       storage.EncryptedStore
	preferences *preferences.Store
	secrets     interface {
		Put(context.Context, string, []byte) error
		Delete(context.Context, string) error
	}
	groupID, accessGroup string
	grants               *localapi.GrantStore
	grantID              string
	grantExpiresAt       time.Time
	previousGrantID      string
	previousGrantUntil   time.Time
	clock                func() time.Time
	onStatus             func(string)
	renewCancel          context.CancelFunc
	renewDone            chan struct{}
	server               *browsercapture.UnixServer
	enabled              atomic.Bool
	stopping             atomic.Bool
}

// renewSafariGrant keeps the existing opt-in/client/scope unchanged. Publishing
// the new credential precedes revoking the old one; a bounded overlap lets an
// in-flight extension request finish without extending consent past Stop.
func (b *safariCaptureBridge) renewSafariGrant(ctx context.Context) (string, error) {
	if b == nil || ctx == nil || ctx.Err() != nil {
		return "", localapi.ErrCanceled
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopping.Load() || b.grants == nil || b.secrets == nil {
		return "", localapi.ErrCanceled
	}
	now := time.Now()
	if b.clock != nil {
		now = b.clock()
	}
	if b.previousGrantID != "" && !now.Before(b.previousGrantUntil) {
		_ = b.grants.Revoke(b.previousGrantID)
		b.previousGrantID = ""
	}
	if now.Before(b.grantExpiresAt.Add(-safariGrantRenewBefore)) {
		return "", nil
	}
	status := safariGrantRenewalFailedStatus
	if !now.Before(b.grantExpiresAt) {
		status = safariGrantExpiredStatus
		b.enabled.Store(false)
	}
	grant, err := b.grants.Issue("safari-extension", []localapi.Scope{localapi.ScopeCaptureIngest}, safariGrantLifetime)
	if err != nil {
		return status, localapi.ErrEndpointUnavailable
	}
	defer clear(grant.Credential)
	committed := false
	defer func() {
		if !committed {
			_ = b.grants.Revoke(grant.ID)
		}
	}()
	record := safariGrantRecord{ID: grant.ID, ClientID: grant.ClientID, Credential: grant.Credential, ExpiresAt: grant.ExpiresAt}
	encoded, err := json.Marshal(record)
	if err != nil {
		return status, localapi.ErrEndpointUnavailable
	}
	defer clear(encoded)
	writeCtx, cancel := context.WithTimeout(ctx, safariCleanupTimeout)
	defer cancel()
	if err := b.secrets.Put(writeCtx, safariGrantSecretName, encoded); err != nil {
		return status, localapi.ErrEndpointUnavailable
	}
	if ctx.Err() != nil || b.stopping.Load() {
		return "", localapi.ErrCanceled
	}
	if b.previousGrantID != "" {
		_ = b.grants.Revoke(b.previousGrantID)
	}
	b.previousGrantID, b.previousGrantUntil = "", time.Time{}
	if now.Before(b.grantExpiresAt) {
		b.previousGrantID, b.previousGrantUntil = b.grantID, minSafariTime(now.Add(safariGrantOverlap), b.grantExpiresAt)
	} else {
		_ = b.grants.Revoke(b.grantID)
	}
	b.grantID, b.grantExpiresAt = grant.ID, grant.ExpiresAt
	committed = true
	b.enabled.Store(!b.stopping.Load())
	return safariCaptureReadyStatus, nil
}

func minSafariTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (b *safariCaptureBridge) runGrantRenewal(ctx context.Context, done chan struct{}, ticks <-chan time.Time) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			b.enabled.Store(false)
			return
		case <-ticks:
			status, _ := b.renewSafariGrant(ctx)
			if status != "" && ctx.Err() == nil && !b.stopping.Load() && b.onStatus != nil {
				b.onStatus(status)
			}
		}
	}
}

func newSafariCaptureBridge(store storage.EncryptedStore, status ...func(string)) *safariCaptureBridge {
	if store == nil || darwin.SafariAppGroupID == "" || darwin.SafariKeychainAccessGroup == "" {
		return nil
	}
	bridge := &safariCaptureBridge{store: store, groupID: darwin.SafariAppGroupID, accessGroup: darwin.SafariKeychainAccessGroup, clock: time.Now}
	if len(status) > 0 {
		bridge.onStatus = status[0]
	}
	return bridge
}

func (b *safariCaptureBridge) Start(ctx context.Context) error {
	if b == nil || ctx == nil || ctx.Err() != nil {
		return localapi.ErrCanceled
	}
	if b.preferences == nil {
		return localapi.ErrEndpointUnavailable
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.server != nil || b.grants != nil {
		return localapi.ErrEndpointInUse
	}
	b.stopping.Store(false)
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
	grants, err := localapi.NewGrantStore(b.clock, nil, 10000)
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
	receiver, err := browsercapture.NewReceiver(grants, b.store, b.enabled.Load, time.Now, safariCapturePreferencePolicy(b.preferences))
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
	b.grantExpiresAt = grant.ExpiresAt
	renewCtx, renewCancel := context.WithCancel(ctx)
	b.renewCancel, b.renewDone = renewCancel, make(chan struct{})
	done := b.renewDone
	go func() {
		ticker := time.NewTicker(safariGrantRenewInterval)
		defer ticker.Stop()
		b.runGrantRenewal(renewCtx, done, ticker.C)
	}()
	return nil
}

func (b *safariCaptureBridge) Stop(ctx context.Context) error {
	if b == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.stopping.Store(true)
	b.enabled.Store(false)
	b.mu.Lock()
	if b.renewCancel != nil {
		b.renewCancel()
	}
	done := b.renewDone
	b.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return localapi.ErrCanceled
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.enabled.Store(false)
	b.renewCancel, b.renewDone = nil, nil
	var err error
	if b.server != nil {
		err = b.server.Stop()
		b.server = nil
	}
	if b.grants != nil && b.grantID != "" {
		_ = b.grants.Revoke(b.grantID)
		if b.previousGrantID != "" {
			_ = b.grants.Revoke(b.previousGrantID)
		}
		b.grants = nil
		b.grantID = ""
		b.previousGrantID = ""
		b.grantExpiresAt, b.previousGrantUntil = time.Time{}, time.Time{}
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
		visual := &bundledVisualText{}
		pipeline := capture.NewDarwinPipelineWithVisualText(source, capture.DefaultLimits(), captureExclusionsFromPreferences(config), nil, visual)
		pipeline.CurrentExclusions = func(ctx context.Context) (capture.Exclusions, error) {
			current, err := a.Client.ApplicationPreferences(ctx)
			if err != nil {
				return capture.Exclusions{}, err
			}
			if !current.CaptureEnabled {
				return capture.Exclusions{}, &capture.CaptureError{Code: capture.Excluded}
			}
			return captureExclusionsFromPreferences(current), nil
		}
		pipeline.VisualAdmission = nativeCaptureVisualAdmission(a.Client.preferences)
		sink := &nativeCaptureCommitSink{store: a.store, preferences: a.Client.preferences}
		service, err := ingest.NewWithActivityAndObservationNotify(pipeline, sink, a.activity, sink, a.activityEpoch, slog.Default(), a.Client.NotifyCaptureChanged, a.notifyChronicleEvidence)
		if err != nil {
			return nil, err
		}
		service.SetCaptureCycleNotify(a.Client.NotifyCaptureStateChanged)
		service.SetHealthNotify(a.Client.setCaptureHealth)
		service.SetDocumentNotify(a.notifyDocumentEvidence)
		service.SetPeriodicRefresh(6 * time.Second)
		return func(ctx context.Context) error {
			defer func() { _ = visual.Close() }()
			if !acceptance.Enabled && shouldRunSafariPageCapture(config.CaptureEnabled, config.SafariPageCaptureEnabled, safariPageCaptureBuildConfigured()) {
				setSafariStatus(a.Client, safariCaptureStartingStatus)
				bridge := newSafariCaptureBridge(a.store, func(status string) { setSafariStatus(a.Client, status) })
				if bridge != nil {
					bridge.preferences = a.Client.preferences
				}
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
