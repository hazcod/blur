// Package localapi defines scoped authorization and message-oriented local
// transport contracts. Native endpoint access control belongs to later adapters.
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

func (g IssuedGrant) String() string { return "issued grant (credential redacted)" }
func (g IssuedGrant) GoString() string { return g.String() }

type Proof struct {
	GrantID   string
	ClientID  string
	Scope     Scope
	Timestamp time.Time
	Nonce     []byte   `json:"-"`
	MAC       [32]byte `json:"-"`
}

func (p Proof) String() string { return "local proof (secret redacted)" }
func (p Proof) GoString() string { return p.String() }

type storedGrant struct {
	clientID  string
	scopes    map[Scope]bool
	issuedAt  time.Time
	expiresAt time.Time
	key       [32]byte
	revoked   bool
	nonces    map[[NonceBytes]byte]struct{}
}

type GrantStore struct {
	mu        sync.Mutex
	now       func() time.Time
	random    io.Reader
	maxNonces int
	grants    map[string]*storedGrant
}

// NewGrantStore accepts injected time and randomness for deterministic tests.
// nil values select the real clock and crypto/rand.Reader. Replay capacity is
// bounded per grant and must be positive.
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
	return &GrantStore{now: now, random: random, maxNonces: maxNonces, grants: make(map[string]*storedGrant)}, nil
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
	if s == nil || !validClientID(clientID) || len(scopes) == 0 || ttl <= 0 {
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
	key := sha256.Sum256(credential[:])
	s.grants[id] = &storedGrant{clientID: clientID, scopes: selected, issuedAt: issued, expiresAt: expires, key: key, nonces: make(map[[NonceBytes]byte]struct{})}
	result := IssuedGrant{ID: id, ClientID: clientID, Scopes: append([]Scope(nil), scopes...), IssuedAt: issued, ExpiresAt: expires, Credential: append([]byte(nil), credential[:]...)}
	return result, nil
}

// Revoke invalidates a grant and releases its nonce state.
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
	g.revoked = true
	g.nonces = nil
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
	key := sha256.Sum256(g.Credential)
	p.MAC = sign(key, p, requestDigest)
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

func sign(key [32]byte, p Proof, digest [32]byte) [32]byte {
	h := hmac.New(sha256.New, key[:])
	writeField(h, []byte("blur-local-proof-v1"))
	writeField(h, []byte(p.GrantID))
	writeField(h, []byte(p.ClientID))
	writeField(h, []byte(p.Scope))
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(p.Timestamp.UnixNano()))
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
	g := s.grants[p.GrantID]
	if g == nil || g.revoked || g.clientID != p.ClientID || !g.scopes[p.Scope] {
		return ErrUnauthorized
	}
	now := s.now()
	if !now.Before(g.expiresAt) {
		g.nonces = nil
		return ErrUnauthorized
	}
	if now.Before(g.issuedAt) || p.Timestamp.Before(g.issuedAt) || !p.Timestamp.Before(g.expiresAt) || p.Timestamp.Before(now.Add(-MaxClockSkew)) || p.Timestamp.After(now.Add(MaxClockSkew)) {
		return ErrUnauthorized
	}
	expected := sign(g.key, p, requestDigest)
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
	g.nonces[nonce] = struct{}{}
	return nil
}
