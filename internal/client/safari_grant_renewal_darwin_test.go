package client

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/localapi"
)

type syntheticSafariSecrets struct {
	value   []byte
	fail    bool
	writes  int
	deleted bool
}

func (s *syntheticSafariSecrets) Put(_ context.Context, _ string, value []byte) error {
	s.writes++
	if s.fail {
		return errors.New("synthetic secret write failure")
	}
	s.value = append(s.value[:0], value...)
	return nil
}
func (s *syntheticSafariSecrets) Delete(context.Context, string) error {
	clear(s.value)
	s.value = nil
	s.deleted = true
	return nil
}

func TestSafariGrantRotatesBeforeExpiryWithoutWideningScopeAndStops(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	grants, _ := localapi.NewGrantStore(func() time.Time { return now }, nil, 100)
	old, _ := grants.Issue("safari-extension", []localapi.Scope{localapi.ScopeCaptureIngest}, safariGrantLifetime)
	secrets := &syntheticSafariSecrets{}
	bridge := &safariCaptureBridge{grants: grants, secrets: secrets, grantID: old.ID, grantExpiresAt: old.ExpiresAt, clock: func() time.Time { return now }}
	bridge.enabled.Store(true)
	if status, err := bridge.renewSafariGrant(context.Background()); err != nil || status != "" || secrets.writes != 0 {
		t.Fatal("grant rotated before renewal interval")
	}
	now = now.Add(23*time.Hour + time.Minute)
	status, err := bridge.renewSafariGrant(context.Background())
	if err != nil || bridge.grantID == old.ID || status != safariCaptureReadyStatus || secrets.writes != 1 {
		t.Fatalf("active opted-in grant did not rotate before expiry: %v", err)
	}
	var published safariGrantRecord
	if err := json.Unmarshal(secrets.value, &published); err != nil {
		t.Fatal(err)
	}
	if published.ID != bridge.grantID || published.ClientID != "safari-extension" || len(published.Credential) != 32 || !published.ExpiresAt.After(old.ExpiresAt) {
		t.Fatal("replacement credential not atomically published")
	}
	var digest [32]byte
	digest[0] = 1
	proof, err := localapi.NewProof(old, localapi.ScopeCaptureIngest, digest, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := grants.Authorize(proof, digest); err != nil {
		t.Fatal("rotation rejected old in-flight credential before overlap ended")
	}
	now = now.Add(3 * time.Minute)
	_, _ = bridge.renewSafariGrant(context.Background())
	proof, _ = localapi.NewProof(old, localapi.ScopeCaptureIngest, digest, now, nil)
	if !errors.Is(grants.Authorize(proof, digest), localapi.ErrUnauthorized) {
		t.Fatal("previous grant survived bounded rotation overlap")
	}
	current := localapi.IssuedGrant{ID: published.ID, ClientID: published.ClientID, Credential: published.Credential, ExpiresAt: published.ExpiresAt, IssuedAt: now.Add(-3 * time.Minute), Scopes: []localapi.Scope{localapi.ScopeCaptureIngest}}
	forbidden := current
	forbidden.Scopes = []localapi.Scope{localapi.ScopeMCPBoundedRead}
	proof, err = localapi.NewProof(forbidden, localapi.ScopeMCPBoundedRead, digest, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(grants.Authorize(proof, digest), localapi.ErrUnauthorized) {
		t.Fatal("renewal widened Safari grant scope")
	}
	if err := bridge.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	proof, _ = localapi.NewProof(current, localapi.ScopeCaptureIngest, digest, now, nil)
	if !errors.Is(grants.Authorize(proof, digest), localapi.ErrUnauthorized) || !secrets.deleted || bridge.enabled.Load() {
		t.Fatal("stop left a live grant, credential or enabled receiver")
	}
	before := secrets.writes
	_, _ = bridge.renewSafariGrant(context.Background())
	if secrets.writes != before {
		t.Fatal("stopped bridge renewed permission")
	}
}

func TestSafariGrantBackgroundFailureExpiryRecoveryAndLifecycle(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(base.UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	grants, _ := localapi.NewGrantStore(now, nil, 100)
	old, _ := grants.Issue("safari-extension", []localapi.Scope{localapi.ScopeCaptureIngest}, safariGrantLifetime)
	defer clear(old.Credential)
	secrets := &syntheticSafariSecrets{fail: true}
	statuses := make(chan string, 4)
	bridge := &safariCaptureBridge{grants: grants, secrets: secrets, grantID: old.ID, grantExpiresAt: old.ExpiresAt, clock: now, onStatus: func(status string) { statuses <- status }}
	bridge.enabled.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bridge.renewCancel, bridge.renewDone = cancel, make(chan struct{})
	ticks := make(chan time.Time, 1)
	go bridge.runGrantRenewal(ctx, bridge.renewDone, ticks)
	waitStatus := func(want string) {
		t.Helper()
		select {
		case got := <-statuses:
			if got != want {
				t.Fatalf("renewal status = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("background renewal status not delivered")
		}
	}
	clock.Store(base.Add(23*time.Hour + time.Minute).UnixNano())
	ticks <- now()
	waitStatus(safariGrantRenewalFailedStatus)
	if bridge.grantID != old.ID || !bridge.enabled.Load() {
		t.Fatal("failed rotation revoked still-valid grant")
	}
	clock.Store(base.Add(24*time.Hour + time.Minute).UnixNano())
	ticks <- now()
	waitStatus(safariGrantExpiredStatus)
	if bridge.enabled.Load() || bridge.grantID != old.ID {
		t.Fatal("expired failed grant remained capture-enabled or was falsely replaced")
	}
	secrets.fail = false
	ticks <- now()
	waitStatus(safariCaptureReadyStatus)
	if !bridge.enabled.Load() || bridge.grantID == old.ID {
		t.Fatal("explicitly opted-in lifecycle did not recover after renewal failure")
	}
	if err := bridge.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := secrets.writes
	ticks <- base.Add(48 * time.Hour)
	if bridge.enabled.Load() || !secrets.deleted || secrets.writes != before {
		t.Fatal("stopped background bridge performed late renewal")
	}
}
