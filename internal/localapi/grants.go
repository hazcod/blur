// Package localapi defines scoped authorization and bounded message-oriented
// local transports. UnixTransport enforces owner-only socket paths and
// same-user peer credentials on macOS and Linux; Windows named pipes remain a
// separate native adapter.
package localapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"
)

type Scope string

const (
	ScopeCaptureIngest  Scope = "capture:ingest"
	ScopeUIRead         Scope = "ui:read"
	ScopeUIMutate       Scope = "ui:mutate"
	ScopeMCPBoundedRead Scope = "mcp:bounded-read"
	ScopeCLIAdmin       Scope = "cli:admin"
	NonceBytes                = 24
	MaxClockSkew              = 30 * time.Second
	MaxGrants                 = 1024
	MaxGrantLifetime          = 365 * 24 * time.Hour
)

var (
	ErrInvalidGrant = errors.New("invalid grant")
	ErrUnauthorized = errors.New("unauthorized")
	ErrReplay       = errors.New("proof replayed")
	ErrReplayLimit  = errors.New("proof replay capacity reached")
)

func (s Scope) Valid() bool {
	switch s {
	case ScopeCaptureIngest, ScopeUIRead, ScopeUIMutate, ScopeMCPBoundedRead, ScopeCLIAdmin:
		return true
	default:
		return false
	}
}

// IssuedGrant is handed to a single client once. Credential is never retained
// by GrantStore or serialized. Callers must protect this bearer material.
type IssuedGrant struct {
	ID         string
	ClientID   string
	Scopes     []Scope
	IssuedAt   time.Time
	ExpiresAt  time.Time
	Credential []byte `json:"-"`
}

func (g IssuedGrant) String() string   { return "issued grant (credential redacted)" }
func (g IssuedGrant) GoString() string { return g.String() }

// MarshalJSON exposes no fields. A future wire protocol needs its own explicit,
// bounded representation.
func (g IssuedGrant) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

type Proof struct {
	GrantID   string
	ClientID  string
	Scope     Scope
	Timestamp time.Time
	Nonce     []byte   `json:"-"`
	MAC       [32]byte `json:"-"`
}

func (p Proof) String() string   { return "local proof (secret redacted)" }
func (p Proof) GoString() string { return p.String() }

// MarshalJSON exposes no fields, including identifiers.
func (p Proof) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

type storedGrant struct {
	clientID  string
	scopes    map[Scope]bool
	issuedAt  time.Time
	expiresAt time.Time
	// This SHA-256 digest is the HMAC authenticator key. Disclosure permits
	// proof forgery, so it is credential-equivalent secret material.
	authenticatorKey [32]byte
	revoked          bool
	nonces           map[[NonceBytes]byte]time.Time
}

func (g *storedGrant) String() string   { return "stored grant (authenticator key redacted)" }
func (g *storedGrant) GoString() string { return g.String() }

type GrantStore struct {
	mu        sync.Mutex
	now       func() time.Time
	random    io.Reader
	maxNonces int
	maxGrants int
	grants    map[string]*storedGrant
}

func (s *GrantStore) String() string   { return "local grant store (authenticator keys redacted)" }
func (s *GrantStore) GoString() string { return s.String() }

// NewGrantStore accepts injected time and randomness for deterministic tests.
// nil values select the real clock and crypto/rand.Reader. Replay capacity is
// bounded per grant and must be positive. The store also enforces MaxGrants.
func NewGrantStore(now func() time.Time, random io.Reader, maxNonces int) (*GrantStore, error) {
	if maxNonces <= 0 {
		return nil, ErrInvalidGrant
	}
	if now == nil {
		now = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	return &GrantStore{now: now, random: random, maxNonces: maxNonces, maxGrants: MaxGrants, grants: make(map[string]*storedGrant)}, nil
}

func clearGrant(g *storedGrant) {
	if g == nil {
		return
	}
	clear(g.scopes)
	clear(g.nonces)
	g.scopes = nil
	g.nonces = nil
	for i := range g.authenticatorKey {
		g.authenticatorKey[i] = 0
	}
	g.clientID = ""
	g.issuedAt = time.Time{}
	g.expiresAt = time.Time{}
	g.revoked = true
}

func (s *GrantStore) removeGrant(id string, g *storedGrant) {
	clearGrant(g)
	if s.grants[id] == g {
		delete(s.grants, id)
	}
}

func (s *GrantStore) pruneLocked(now time.Time) {
	for id, grant := range s.grants {
		if grant.revoked || !now.Before(grant.expiresAt) {
			s.removeGrant(id, grant)
			continue
		}
		// Proof timestamps on the exact skew boundary are still accepted, so
		// retain each replay key until it is strictly outside that window.
		for nonce, timestamp := range grant.nonces {
			if timestamp.IsZero() || now.After(timestamp.Add(MaxClockSkew)) {
				delete(grant.nonces, nonce)
			}
		}
	}
}

func validClientID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

func (s *GrantStore) Issue(clientID string, scopes []Scope, ttl time.Duration) (IssuedGrant, error) {
	if s == nil || !validClientID(clientID) || len(scopes) == 0 || ttl <= 0 || ttl > MaxGrantLifetime {
		return IssuedGrant{}, ErrInvalidGrant
	}
	selected := make(map[Scope]bool, len(scopes))
	for _, scope := range scopes {
		if !scope.Valid() || selected[scope] {
			return IssuedGrant{}, ErrInvalidGrant
		}
		selected[scope] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	issued := s.now()
	expires := issued.Add(ttl)
	if issued.IsZero() || !expires.After(issued) {
		return IssuedGrant{}, ErrInvalidGrant
	}
	s.pruneLocked(issued)
	if len(s.grants) >= s.maxGrants {
		return IssuedGrant{}, ErrInvalidGrant
	}
	var rawID [16]byte
	var credential [32]byte
	if _, err := io.ReadFull(s.random, rawID[:]); err != nil {
		return IssuedGrant{}, ErrInvalidGrant
	}
	if _, err := io.ReadFull(s.random, credential[:]); err != nil {
		return IssuedGrant{}, ErrInvalidGrant
	}
	if allZero(credential[:]) {
		return IssuedGrant{}, ErrInvalidGrant
	}
	id := hex.EncodeToString(rawID[:])
	if _, exists := s.grants[id]; exists {
		return IssuedGrant{}, ErrInvalidGrant
	}
	authenticatorKey := sha256.Sum256(credential[:])
	s.grants[id] = &storedGrant{clientID: clientID, scopes: selected, issuedAt: issued, expiresAt: expires, authenticatorKey: authenticatorKey, nonces: make(map[[NonceBytes]byte]time.Time)}
	result := IssuedGrant{ID: id, ClientID: clientID, Scopes: append([]Scope(nil), scopes...), IssuedAt: issued, ExpiresAt: expires, Credential: append([]byte(nil), credential[:]...)}
	return result, nil
}

// Restore reconstructs a live grant from an owner-protected credential record.
// The credential itself is never retained; only its derived HMAC key is kept.
func (s *GrantStore) Restore(grant IssuedGrant) error {
	if s == nil || !validGrantID(grant.ID) || grant.ClientID != "claude.mcp" || len(grant.Scopes) != 1 || grant.Scopes[0] != ScopeMCPBoundedRead || len(grant.Credential) != 32 || allZero(grant.Credential) || grant.IssuedAt.IsZero() || !grant.ExpiresAt.After(grant.IssuedAt) || grant.ExpiresAt.Sub(grant.IssuedAt) > MaxGrantLifetime {
		return ErrInvalidGrant
	}
	selected := map[Scope]bool{ScopeMCPBoundedRead: true}
	now := s.now()
	if now.IsZero() || !now.Before(grant.ExpiresAt) || grant.IssuedAt.After(now.Add(MaxClockSkew)) {
		return ErrInvalidGrant
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	if len(s.grants) >= s.maxGrants {
		return ErrInvalidGrant
	}
	if _, exists := s.grants[grant.ID]; exists {
		return ErrInvalidGrant
	}
	s.grants[grant.ID] = &storedGrant{
		clientID: grant.ClientID, scopes: selected, issuedAt: grant.IssuedAt,
		expiresAt: grant.ExpiresAt, authenticatorKey: sha256.Sum256(grant.Credential),
		nonces: make(map[[NonceBytes]byte]time.Time),
	}
	return nil
}

func validGrantID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for i := range id {
		if !((id[i] >= '0' && id[i] <= '9') || (id[i] >= 'a' && id[i] <= 'f')) {
			return false
		}
	}
	return true
}

// Revoke invalidates and removes a grant after clearing its replay and key data.
func (s *GrantStore) Revoke(id string) error {
	if s == nil {
		return ErrUnauthorized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.grants[id]
	if g == nil {
		return ErrUnauthorized
	}
	s.removeGrant(id, g)
	return nil
}

// NewProof creates a one-request proof. The caller-provided digest must bind
// the exact request bytes and semantics the verifier is asked to authorize.
func NewProof(g IssuedGrant, scope Scope, requestDigest [32]byte, at time.Time, random io.Reader) (Proof, error) {
	if !validClientID(g.ClientID) || g.ID == "" || !scope.Valid() || len(g.Credential) != 32 || requestDigest == ([32]byte{}) || at.IsZero() {
		return Proof{}, ErrUnauthorized
	}
	if random == nil {
		random = rand.Reader
	}
	p := Proof{GrantID: g.ID, ClientID: g.ClientID, Scope: scope, Timestamp: at, Nonce: make([]byte, NonceBytes)}
	if _, err := io.ReadFull(random, p.Nonce); err != nil {
		return Proof{}, ErrUnauthorized
	}
	if allZero(p.Nonce) {
		return Proof{}, ErrUnauthorized
	}
	authenticatorKey := sha256.Sum256(g.Credential)
	p.MAC = sign(authenticatorKey, p, requestDigest)
	return p, nil
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

func writeField(h io.Writer, b []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(b)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(b)
}

func sign(authenticatorKey [32]byte, p Proof, digest [32]byte) [32]byte {
	h := hmac.New(sha256.New, authenticatorKey[:])
	writeField(h, []byte("blur-local-proof-v1"))
	writeField(h, []byte(p.GrantID))
	writeField(h, []byte(p.ClientID))
	writeField(h, []byte(p.Scope))
	// Signed Unix seconds plus canonical nanoseconds cover the full time.Time
	// range without the overflow and collisions of UnixNano.
	var ts [12]byte
	binary.BigEndian.PutUint64(ts[:8], uint64(p.Timestamp.Unix()))
	binary.BigEndian.PutUint32(ts[8:], uint32(p.Timestamp.Nanosecond()))
	writeField(h, ts[:])
	writeField(h, p.Nonce)
	writeField(h, digest[:])
	var mac [32]byte
	copy(mac[:], h.Sum(nil))
	return mac
}

// Authorize checks all bindings and consumes the nonce only after a valid MAC.
func (s *GrantStore) Authorize(p Proof, requestDigest [32]byte) error {
	if s == nil || !validClientID(p.ClientID) || len(p.GrantID) != 32 || !p.Scope.Valid() || p.Timestamp.IsZero() || len(p.Nonce) != NonceBytes || allZero(p.Nonce) || requestDigest == ([32]byte{}) {
		return ErrUnauthorized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	g := s.grants[p.GrantID]
	if g == nil || g.revoked || g.clientID != p.ClientID || !g.scopes[p.Scope] {
		return ErrUnauthorized
	}
	if now.Before(g.issuedAt) || p.Timestamp.Before(g.issuedAt) || !p.Timestamp.Before(g.expiresAt) || p.Timestamp.Before(now.Add(-MaxClockSkew)) || p.Timestamp.After(now.Add(MaxClockSkew)) {
		return ErrUnauthorized
	}
	expected := sign(g.authenticatorKey, p, requestDigest)
	if !hmac.Equal(expected[:], p.MAC[:]) {
		return ErrUnauthorized
	}
	var nonce [NonceBytes]byte
	copy(nonce[:], p.Nonce)
	if _, exists := g.nonces[nonce]; exists {
		return ErrReplay
	}
	if len(g.nonces) >= s.maxNonces {
		return ErrReplayLimit
	}
	g.nonces[nonce] = p.Timestamp
	return nil
}
