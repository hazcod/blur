package localapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type grantSequenceReader struct{ next uint64 }

func (r *grantSequenceReader) Read(p []byte) (int, error) {
	switch len(p) {
	case 16:
		r.next++
		binary.BigEndian.PutUint64(p[:8], r.next)
		binary.BigEndian.PutUint64(p[8:], ^r.next)
	case 32:
		for i := range p {
			p[i] = 0x81
		}
	default:
		for i := range p {
			p[i] = 0x41
		}
	}
	return len(p), nil
}

func grantFixture(t *testing.T, cap int) (*GrantStore, IssuedGrant, *time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	store, err := NewGrantStore(func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x31}, 4096)), cap)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := store.Issue("client_1", []Scope{ScopeUIRead, ScopeUIMutate}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return store, grant, &now
}

func proofFixture(t *testing.T, grant IssuedGrant, scope Scope, digest [32]byte, at time.Time, seed byte) Proof {
	t.Helper()
	proof, err := NewProof(grant, scope, digest, at, bytes.NewReader(bytes.Repeat([]byte{seed}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func TestGrantProofIsSingleUseAndRequestBound(t *testing.T) {
	store, grant, now := grantFixture(t, 2)
	digest := sha256.Sum256([]byte("request A"))
	proof := proofFixture(t, grant, ScopeUIRead, digest, *now, 1)
	if err := store.Authorize(proof, digest); err != nil {
		t.Fatal(err)
	}
	if err := store.Authorize(proof, digest); !errors.Is(err, ErrReplay) {
		t.Fatalf("replay: %v", err)
	}
	other := proofFixture(t, grant, ScopeUIRead, digest, *now, 2)
	if err := store.Authorize(other, sha256.Sum256([]byte("request B"))); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("request mismatch: %v", err)
	}
	if err := store.Authorize(other, digest); err != nil {
		t.Fatalf("invalid attempt consumed nonce: %v", err)
	}
}

func TestGrantRejectsTamperingAndWrongBindings(t *testing.T) {
	store, grant, now := grantFixture(t, 8)
	digest := sha256.Sum256([]byte("payload"))
	base := proofFixture(t, grant, ScopeUIRead, digest, *now, 3)
	tests := []struct {
		name   string
		change func(*Proof)
	}{
		{"client", func(p *Proof) { p.ClientID = "other" }},
		{"scope", func(p *Proof) { p.Scope = ScopeUIMutate }},
		{"grant", func(p *Proof) { p.GrantID = "other" }},
		{"timestamp", func(p *Proof) { p.Timestamp = p.Timestamp.Add(time.Second) }},
		{"nonce", func(p *Proof) { p.Nonce[0] ^= 1 }},
		{"mac", func(p *Proof) { p.MAC[0] ^= 1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.Nonce = append([]byte(nil), base.Nonce...)
			tc.change(&p)
			if err := store.Authorize(p, digest); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("got %v", err)
			}
		})
	}
	if err := store.Authorize(base, digest); err != nil {
		t.Fatalf("tampering consumed nonce: %v", err)
	}
	if err := store.Authorize(proofFixture(t, grant, ScopeCLIAdmin, digest, *now, 4), digest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ungranted scope: %v", err)
	}
}

func TestGrantExpiryRevocationSkewAndReplayBound(t *testing.T) {
	store, grant, now := grantFixture(t, 1)
	digest := sha256.Sum256([]byte("payload"))
	if err := store.Authorize(proofFixture(t, grant, ScopeUIRead, digest, *now, 5), digest); err != nil {
		t.Fatal(err)
	}
	if err := store.Authorize(proofFixture(t, grant, ScopeUIRead, digest, *now, 6), digest); !errors.Is(err, ErrReplayLimit) {
		t.Fatalf("bound: %v", err)
	}
	if err := store.Revoke(grant.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Authorize(proofFixture(t, grant, ScopeUIRead, digest, *now, 7), digest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked: %v", err)
	}
	store2, grant2, now2 := grantFixture(t, 3)
	*now2 = grant2.ExpiresAt
	if err := store2.Authorize(proofFixture(t, grant2, ScopeUIRead, digest, *now2, 8), digest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expiry boundary: %v", err)
	}
	store3, grant3, now3 := grantFixture(t, 3)
	if err := store3.Authorize(proofFixture(t, grant3, ScopeUIRead, digest, now3.Add(MaxClockSkew+time.Nanosecond), 9), digest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("skew: %v", err)
	}
}

func TestGrantRejectsInvalidInputsAndDoesNotLeak(t *testing.T) {
	store, grant, now := grantFixture(t, 3)
	for _, scopes := range [][]Scope{nil, {"unknown"}, {ScopeUIRead, ScopeUIRead}} {
		if _, err := store.Issue("client_2", scopes, time.Minute); !errors.Is(err, ErrInvalidGrant) {
			t.Fatalf("scopes %v: %v", scopes, err)
		}
	}
	if _, err := store.Issue("client / secret", []Scope{ScopeUIRead}, time.Minute); !errors.Is(err, ErrInvalidGrant) {
		t.Fatal(err)
	}
	if _, err := store.Issue("client_2", []Scope{ScopeUIRead}, 0); !errors.Is(err, ErrInvalidGrant) {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("payload"))
	for _, p := range []Proof{{}, {GrantID: grant.ID, ClientID: grant.ClientID, Scope: ScopeUIRead, Timestamp: *now, Nonce: []byte{1}}, {GrantID: grant.ID, ClientID: grant.ClientID, Scope: ScopeUIRead, Timestamp: *now, Nonce: bytes.Repeat([]byte{0}, NonceBytes)}} {
		if err := store.Authorize(p, digest); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("malformed: %v", err)
		}
	}
	if err := store.Authorize(proofFixture(t, grant, ScopeUIRead, digest, *now, 10), [32]byte{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("zero digest: %v", err)
	}
	for _, err := range []error{ErrInvalidGrant, ErrUnauthorized, ErrReplay, ErrReplayLimit} {
		if strings.Contains(err.Error(), grant.ID) || strings.Contains(err.Error(), grant.ClientID) || strings.Contains(err.Error(), string(grant.Credential)) {
			t.Fatalf("leak: %v", err)
		}
	}
	encoded, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, grant.Credential) {
		t.Fatal("credential serialized")
	}
	if text := fmt.Sprintf("%#v", grant); strings.Contains(text, "Credential") || strings.Contains(text, "[49 49") {
		t.Fatalf("credential formatted: %s", text)
	}
}

func TestGrantRejectsZeroClockAndZeroCredential(t *testing.T) {
	zeroClock, err := NewGrantStore(func() time.Time { return time.Time{} }, bytes.NewReader(bytes.Repeat([]byte{1}, 64)), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zeroClock.Issue("client", []Scope{ScopeUIRead}, time.Minute); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("zero clock: %v", err)
	}
	zeroSecret := append(bytes.Repeat([]byte{1}, 16), bytes.Repeat([]byte{0}, 32)...)
	store, err := NewGrantStore(func() time.Time { return time.Now() }, bytes.NewReader(zeroSecret), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Issue("client", []Scope{ScopeUIRead}, time.Minute); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("zero credential: %v", err)
	}
}

func TestGrantAndProofJSONAreFullyRedacted(t *testing.T) {
	_, grant, now := grantFixture(t, 2)
	proof := proofFixture(t, grant, ScopeUIRead, sha256.Sum256([]byte("request")), *now, 41)
	for name, value := range map[string]any{"grant": grant, "proof": proof, "grant pointer": &grant, "proof pointer": &proof} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(encoded) != "{}" {
			t.Fatalf("%s exposed fields: %s", name, encoded)
		}
	}
}

func TestGrantStoreFormattingRedactsAuthenticatorKey(t *testing.T) {
	store, grant, _ := grantFixture(t, 2)
	for name, value := range map[string]any{"store": store, "stored grant": store.grants[grant.ID]} {
		for _, formatted := range []string{fmt.Sprintf("%v", value), fmt.Sprintf("%+v", value), fmt.Sprintf("%#v", value)} {
			if strings.Contains(formatted, grant.ID) || strings.Contains(formatted, grant.ClientID) || strings.Contains(formatted, "authenticatorKey") || strings.Contains(formatted, "[32]uint8") {
				t.Fatalf("%s exposed internal material: %s", name, formatted)
			}
		}
	}
}

func TestProofTimestampEncodingDistinguishesExtremeInstants(t *testing.T) {
	var key [32]byte
	var digest [32]byte
	key[0], digest[0] = 1, 2
	p := Proof{GrantID: strings.Repeat("a", 32), ClientID: "client", Scope: ScopeUIRead, Nonce: bytes.Repeat([]byte{3}, NonceBytes)}
	instants := []time.Time{
		time.Unix(-1, 999999999),
		time.Unix(0, 0),
		time.Unix(0, 1),
		time.Unix(18446744073, 709551616), // exactly 2^64 nanoseconds after Unix epoch
		time.Unix(18446744073, 709551617),
	}
	seen := make(map[[32]byte]time.Time)
	for _, instant := range instants {
		p.Timestamp = instant
		mac := sign(key, p, digest)
		if prior, exists := seen[mac]; exists {
			t.Fatalf("timestamp collision: %s and %s", prior, instant)
		}
		seen[mac] = instant
	}
	// Changing only a boundary timestamp must invalidate the original proof.
	store, grant, now := grantFixture(t, 2)
	request := sha256.Sum256([]byte("request"))
	valid := proofFixture(t, grant, ScopeUIRead, request, *now, 42)
	valid.Timestamp = valid.Timestamp.Add(time.Nanosecond)
	if err := store.Authorize(valid, request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("timestamp tamper: %v", err)
	}
}

func TestGrantCredentialAndScopeCopies(t *testing.T) {
	store, grant, now := grantFixture(t, 3)
	original := append([]byte(nil), grant.Credential...)
	grant.Credential[0] ^= 1
	digest := sha256.Sum256([]byte("payload"))
	if err := store.Authorize(proofFixture(t, grant, ScopeUIRead, digest, *now, 11), digest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("mutated credential: %v", err)
	}
	grant.Credential = original
	grant.Scopes[0] = ScopeCLIAdmin
	if err := store.Authorize(proofFixture(t, grant, ScopeUIRead, digest, *now, 12), digest); err != nil {
		t.Fatalf("server scope mutated: %v", err)
	}
}

func TestConcurrentAuthorizationAcceptsOneProof(t *testing.T) {
	store, grant, now := grantFixture(t, 20)
	digest := sha256.Sum256([]byte("payload"))
	p := proofFixture(t, grant, ScopeUIRead, digest, *now, 13)
	var wg sync.WaitGroup
	results := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- store.Authorize(p, digest) }()
	}
	wg.Wait()
	close(results)
	ok, replay := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrReplay) {
			replay++
		} else {
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || replay != 31 {
		t.Fatalf("accepted=%d replay=%d", ok, replay)
	}
}

func TestInvalidTimestampDoesNotEraseReplayHistory(t *testing.T) {
	store, grant, now := grantFixture(t, 2)
	digest := sha256.Sum256([]byte("request"))
	accepted := proofFixture(t, grant, ScopeUIRead, digest, *now, 14)
	if err := store.Authorize(accepted, digest); err != nil {
		t.Fatal(err)
	}
	late := proofFixture(t, grant, ScopeUIRead, digest, now.Add(MaxClockSkew+time.Second), 15)
	if err := store.Authorize(late, digest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("late: %v", err)
	}
	if err := store.Authorize(accepted, digest); !errors.Is(err, ErrReplay) {
		t.Fatalf("old nonce forgotten: %v", err)
	}
}

func TestGrantStoreRejectsIssuanceAtTheTotalGrantCap(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	store, err := NewGrantStore(func() time.Time { return now }, &grantSequenceReader{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var grants []IssuedGrant
	for i := 0; i < 1024; i++ {
		grant, err := store.Issue("client", []Scope{ScopeUIRead}, time.Hour)
		if err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
		grants = append(grants, grant)
	}
	if _, err := store.Issue("client", []Scope{ScopeUIRead}, time.Hour); err == nil {
		t.Fatal("issuance beyond the total grant cap succeeded")
	}
}

func TestRevokedGrantIsZeroizedRemovedAndItsCapacityReused(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	store, err := NewGrantStore(func() time.Time { return now }, &grantSequenceReader{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var first IssuedGrant
	var retained *storedGrant
	for i := 0; i < 1024; i++ {
		grant, err := store.Issue("client", []Scope{ScopeUIRead}, time.Hour)
		if err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
		if i == 0 {
			first = grant
			retained = store.grants[grant.ID]
		}
	}
	keyBefore := retained.authenticatorKey
	if keyBefore == ([32]byte{}) {
		t.Fatal("fixture authenticator key is zero")
	}
	digest := sha256.Sum256([]byte("revoke-capacity"))
	oldProof := proofFixture(t, first, ScopeUIRead, digest, now, 0x51)
	if err := store.Revoke(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, exists := store.grants[first.ID]; exists {
		t.Fatal("revoked grant remains in the store")
	}
	if retained.authenticatorKey != ([32]byte{}) || retained.scopes != nil || retained.nonces != nil {
		t.Fatal("revoked grant retained secret bytes or map state")
	}
	if err := store.Authorize(oldProof, digest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old proof after revocation: %v", err)
	}
	if _, err := store.Issue("client", []Scope{ScopeUIRead}, time.Hour); err != nil {
		t.Fatalf("revocation did not release grant capacity: %v", err)
	}
}

func TestExpiredGrantIsZeroizedPrunedAndItsCapacityReused(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	store, err := NewGrantStore(func() time.Time { return now }, &grantSequenceReader{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Issue("client", []Scope{ScopeUIRead}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	retained := store.grants[first.ID]
	digest := sha256.Sum256([]byte("expired-capacity"))
	oldProof := proofFixture(t, first, ScopeUIRead, digest, now, 0x52)
	for i := 1; i < 1024; i++ {
		if _, err := store.Issue("client", []Scope{ScopeUIRead}, time.Minute); err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
	}
	now = first.ExpiresAt
	if _, err := store.Issue("client", []Scope{ScopeUIRead}, time.Minute); err != nil {
		t.Fatalf("issuance did not prune expired grants at capacity: %v", err)
	}
	if _, exists := store.grants[first.ID]; exists {
		t.Fatal("expired grant remains in the store")
	}
	if retained.authenticatorKey != ([32]byte{}) || retained.scopes != nil || retained.nonces != nil {
		t.Fatal("pruned grant retained secret bytes or map state")
	}
	if err := store.Authorize(oldProof, digest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired proof: %v", err)
	}
	if len(store.grants) != 1 {
		t.Fatalf("pruning retained %d grants; want only the new active grant", len(store.grants))
	}
}

func TestExpiryAuthorizationZeroizesAndRemovesTheGrant(t *testing.T) {
	store, grant, now := grantFixture(t, 1)
	retained := store.grants[grant.ID]
	digest := sha256.Sum256([]byte("expiry-zeroization"))
	oldProof := proofFixture(t, grant, ScopeUIRead, digest, *now, 0x53)
	*now = grant.ExpiresAt
	if err := store.Authorize(oldProof, digest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired proof: %v", err)
	}
	if _, exists := store.grants[grant.ID]; exists {
		t.Fatal("expired grant remains in the store")
	}
	if retained.authenticatorKey != ([32]byte{}) || retained.scopes != nil || retained.nonces != nil {
		t.Fatal("expired grant retained secret bytes or map state")
	}
}
