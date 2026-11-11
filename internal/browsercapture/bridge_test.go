package browsercapture

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/localapi"
)

type receiverSink struct {
	mu        sync.Mutex
	encrypted bool
	rows      []capture.SanitizedObservation
	started   chan struct{}
	finish    chan struct{}
}

func (s *receiverSink) EncryptedAtRest() bool { return s.encrypted }
func (s *receiverSink) PutObservation(_ context.Context, o capture.SanitizedObservation) error {
	if s.started != nil {
		close(s.started)
		<-s.finish
	}
	s.mu.Lock()
	s.rows = append(s.rows, o)
	s.mu.Unlock()
	return nil
}

type testEnvelope struct {
	Version int             `json:"version"`
	Payload json.RawMessage `json:"payload"`
	Proof   struct {
		GrantID   string    `json:"grant_id"`
		ClientID  string    `json:"client_id"`
		Scope     string    `json:"scope"`
		Timestamp time.Time `json:"timestamp"`
		Nonce     string    `json:"nonce"`
		MAC       string    `json:"mac"`
	} `json:"proof"`
}

func receiverFixture(t *testing.T, now time.Time, enabled bool, sink *receiverSink) (*Receiver, *localapi.GrantStore, []byte) {
	t.Helper()
	grants, err := localapi.NewGrantStore(func() time.Time { return now }, nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.Issue("safari-extension", []localapi.Scope{localapi.ScopeCaptureIngest}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"browser":"com.apple.safari","url":"https://example.test/a?secret=hide","title":"Plan","text":"Owner: Sam","active":true,"privacy_known":true,"private":false,"captured_at":"` + now.Format(time.RFC3339Nano) + `"}`)
	digest := sha256.Sum256(payload)
	proof, err := localapi.NewProof(grant, localapi.ScopeCaptureIngest, digest, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	wireProof, _ := json.Marshal(proof)
	var e testEnvelope
	e.Version = 1
	e.Payload = payload
	_ = json.Unmarshal(wireProof, &e.Proof)
	// Proof deliberately has no JSON representation; encode its explicit protocol fields.
	e.Proof.GrantID, e.Proof.ClientID, e.Proof.Scope, e.Proof.Timestamp = grant.ID, grant.ClientID, string(localapi.ScopeCaptureIngest), now
	e.Proof.Nonce, e.Proof.MAC = base64.RawURLEncoding.EncodeToString(proof.Nonce), hex.EncodeToString(proof.MAC[:])
	wire, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewReceiver(grants, sink, func() bool { return enabled }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return receiver, grants, wire
}

func TestReceiverAcceptsAuthenticatedSnapshotAndPersistsSanitizedObservation(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	sink := &receiverSink{encrypted: true}
	r, _, wire := receiverFixture(t, now, true, sink)
	if err := r.Accept(context.Background(), wire); err != nil {
		t.Fatal(err)
	}
	if len(sink.rows) != 1 || sink.rows[0].Domain() != "example.test" || strings.Contains(sink.rows[0].Text(), "secret=hide") {
		t.Fatalf("unexpected stored row: %#v", sink.rows)
	}
}

func TestReceiverRejectsInvalidOrDisabledRequestsWithoutWriting(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		disabled bool
		tamper   func([]byte) []byte
	}{
		{"disabled", true, nil}, {"bad mac", false, func(b []byte) []byte { return []byte(strings.Replace(string(b), `"mac":"`, `"mac":"0`, 1)) }},
		{"malformed payload", false, func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"text":"Owner: Sam"`, `"text":"Owner: Sam","extra":true`, 1))
		}},
		{"duplicate payload key", false, func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"text":"Owner: Sam"`, `"text":"one","text":"two"`, 1))
		}},
		{"invalid UTF-8", false, func(b []byte) []byte { return append(b, 0xff) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink := &receiverSink{encrypted: true}
			r, _, wire := receiverFixture(t, now, !tc.disabled, sink)
			if tc.tamper != nil {
				wire = tc.tamper(wire)
			}
			if err := r.Accept(context.Background(), wire); err == nil {
				t.Fatal("accepted invalid request")
			}
			if len(sink.rows) != 0 {
				t.Fatal("invalid request reached sink")
			}
		})
	}
	for _, mutate := range []func(*Snapshot){func(p *Snapshot) { p.Private = true }, func(p *Snapshot) { p.Active = false }, func(p *Snapshot) { p.PrivacyKnown = false }} {
		p := Snapshot{Browser: SafariBundleID, URL: "https://example.test", Text: "body", Active: true, PrivacyKnown: true, CapturedAt: now}
		mutate(&p)
		if _, err := Sanitize(p, true, now); err == nil {
			t.Fatal("accepted disallowed privacy state")
		}
	}
}

func TestReceiverRequiresEncryptedSinkAndEnforcesFrameLimit(t *testing.T) {
	now := time.Now().UTC()
	sink := &receiverSink{}
	if _, err := NewReceiver(nil, sink, func() bool { return true }, time.Now); err == nil {
		t.Fatal("accepted unencrypted sink")
	}
	sink.encrypted = true
	r, _, _ := receiverFixture(t, now, true, sink)
	if err := r.Accept(context.Background(), make([]byte, MaxWireBytes+1)); err == nil {
		t.Fatal("accepted oversized frame")
	}
}

func TestReceiverRevokedGrantAndReplayAreRejected(t *testing.T) {
	now := time.Now().UTC()
	sink := &receiverSink{encrypted: true}
	r, grants, wire := receiverFixture(t, now, true, sink)
	if err := r.Accept(context.Background(), wire); err != nil {
		t.Fatal(err)
	}
	if err := r.Accept(context.Background(), wire); err == nil {
		t.Fatal("accepted replay")
	}
	otherSink := &receiverSink{encrypted: true}
	other, otherGrants, otherWire := receiverFixture(t, now, true, otherSink)
	var envelope testEnvelope
	if err := json.Unmarshal(otherWire, &envelope); err != nil {
		t.Fatal(err)
	}
	if err := otherGrants.Revoke(envelope.Proof.GrantID); err != nil {
		t.Fatal(err)
	}
	if err := other.Accept(context.Background(), otherWire); err == nil {
		t.Fatal("accepted revoked grant")
	}
	_ = grants
}

func TestReceiverPauseWaitsForInFlightEncryptedWrite(t *testing.T) {
	now := time.Now().UTC()
	sink := &receiverSink{encrypted: true, started: make(chan struct{}), finish: make(chan struct{})}
	r, _, wire := receiverFixture(t, now, true, sink)
	done := make(chan error, 1)
	go func() { done <- r.Accept(context.Background(), wire) }()
	<-sink.started
	paused := make(chan struct{})
	go func() { r.Pause(); close(paused) }()
	select {
	case <-paused:
		t.Fatal("pause returned before in-flight write completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(sink.finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-paused
	if err := r.Accept(context.Background(), wire); !errors.Is(err, ErrReceiverPaused) {
		t.Fatalf("after pause: %v", err)
	}
}
