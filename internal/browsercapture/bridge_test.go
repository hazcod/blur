package browsercapture

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestReceiverPersistsRepeatedPageVisitWithoutDuplicatingText(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	first := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	visit := first.AddDate(0, 0, 2)
	for _, at := range []time.Time{first, visit} {
		r, _, wire := receiverFixture(t, at, true, &receiverSink{encrypted: true})
		r.sink = store
		if err := r.Accept(ctx, wire); err != nil {
			t.Fatalf("authenticated repeated page rejected: %v", err)
		}
	}
	page, err := store.ListObservationWindow(ctx, storage.ObservationWindowRequest{Start: visit, End: visit.Add(time.Hour), Limit: 5, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 1 || !page.CapturedAt[0].Equal(visit) {
		t.Fatalf("Safari visit invisible: %+v %v", page, err)
	}
	all, err := store.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: 5, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(all.Observations) != 1 || !all.CapturedAt[0].Equal(first) {
		t.Fatalf("Safari text was duplicated or first capture lost: %+v %v", all, err)
	}
}

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
	payload := []byte(`{"browser":"com.apple.safari","url":"https://example.test/a?secret=hide","title":"Plan","text":"Owner: Sam owns the rollback and schedule","active":true,"privacy_known":true,"private":false,"complete":true,"captured_at":"` + now.Format(time.RFC3339Nano) + `"}`)
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
			return []byte(strings.Replace(string(b), `"text":"Owner: Sam owns the rollback and schedule"`, `"text":"Owner: Sam owns the rollback and schedule","extra":true`, 1))
		}},
		{"duplicate payload key", false, func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"text":"Owner: Sam owns the rollback and schedule"`, `"text":"one","text":"two"`, 1))
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
		p := Snapshot{Browser: SafariBundleID, URL: "https://example.test", Text: "body text that is comfortably long enough to keep", Active: true, PrivacyKnown: true, Complete: true, CapturedAt: now}
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

func TestReceiverPolicyRejectionPreventsPersistence(t *testing.T) {
	sink := &receiverSink{encrypted: true}
	r, _, wire := receiverFixture(t, time.Now().UTC(), true, sink)
	var checked bool
	r.policy = func(context.Context, capture.SanitizedObservation, func(context.Context) error) error {
		checked = true
		return ErrRejected
	}
	err := r.Accept(context.Background(), wire)
	if !errors.Is(err, ErrRejected) || !checked || len(sink.rows) != 0 {
		t.Fatalf("excluded Safari page persisted without scope check: checked=%t writes=%d err=%v", checked, len(sink.rows), err)
	}
}

func TestReceiverRevalidatesChangedPolicyAfterPageRead(t *testing.T) {
	now := time.Now().UTC()
	sink := &receiverSink{encrypted: true}
	r, _, wire := receiverFixture(t, now, true, sink)
	read, proceed := make(chan struct{}), make(chan struct{})
	r.clock = func() time.Time { close(read); <-proceed; return now }
	var denied atomic.Bool
	r.policy = func(ctx context.Context, _ capture.SanitizedObservation, write func(context.Context) error) error {
		if denied.Load() {
			return ErrRejected
		}
		return write(ctx)
	}
	done := make(chan error, 1)
	go func() { done <- r.Accept(context.Background(), wire) }()
	<-read
	denied.Store(true)
	close(proceed)
	if err := <-done; !errors.Is(err, ErrRejected) || len(sink.rows) != 0 {
		t.Fatalf("policy changed after read but old page persisted: %v", err)
	}
}

type duplicatePolicySink struct{ writes, sightings int }

func (*duplicatePolicySink) EncryptedAtRest() bool { return true }
func (s *duplicatePolicySink) PutObservation(context.Context, capture.SanitizedObservation) error {
	s.writes++
	return &storage.StoreError{Code: storage.Duplicate}
}
func (s *duplicatePolicySink) RecordObservationSighting(context.Context, capture.SanitizedObservation, time.Time) error {
	s.sightings++
	return nil
}

func TestReceiverPolicyCoversDuplicateSightingAndCanceledCommit(t *testing.T) {
	r, _, wire := receiverFixture(t, time.Now().UTC(), true, &receiverSink{encrypted: true})
	sink := &duplicatePolicySink{}
	r.sink = sink
	var policyActive bool
	r.policy = func(ctx context.Context, _ capture.SanitizedObservation, write func(context.Context) error) error {
		policyActive = true
		err := write(ctx)
		if sink.writes != 1 || sink.sightings != 1 || !policyActive {
			t.Fatal("sighting escaped guarded write callback")
		}
		policyActive = false
		return err
	}
	if err := r.Accept(context.Background(), wire); err != nil {
		t.Fatal(err)
	}
	r, _, wire = receiverFixture(t, time.Now().UTC(), true, &receiverSink{encrypted: true})
	r.sink = sink
	r.policy = func(ctx context.Context, _ capture.SanitizedObservation, write func(context.Context) error) error {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		return write(canceled)
	}
	if err := r.Accept(context.Background(), wire); !errors.Is(err, context.Canceled) || sink.writes != 1 || sink.sightings != 1 {
		t.Fatal("canceled prewrite check persisted content or a sighting")
	}
}

func TestReceiverPreservesShortSnapshotsAndRejectsBlankContent(t *testing.T) {
	ctx := context.Background()
	long := strings.Repeat("x", 32)
	short := strings.Repeat("x", 31)
	for _, c := range []struct {
		text, app string
		stored    bool
	}{{"", "", false}, {" \n\t  ", "", false}, {"", "sh.blur.notes", false}, {" \n", "sh.blur.meetings", false}, {"x", "", true}, {"日", "", true}, {short, "", true}, {long, "", true}, {"hi", "sh.blur.notes", true}, {"hi", "sh.blur.meetings", true}} {
		sink := &receiverSink{encrypted: true}
		r, _, _ := receiverFixture(t, time.Now(), true, sink)
		o, err := capture.SanitizeObservation(firstNonEmpty(c.app, SafariBundleID), "example.test", c.text, 256)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.persist(ctx, o, time.Now()); err != nil {
			t.Fatalf("persist %q: %v", c.text, err)
		}
		sink.mu.Lock()
		rows := len(sink.rows)
		sink.mu.Unlock()
		if (rows == 1) != c.stored || (!c.stored && r.DroppedShort() != 1) || (c.stored && r.DroppedShort() != 0) {
			t.Fatalf("text %q app %q: rows=%d dropped=%d", c.text, c.app, rows, r.DroppedShort())
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
