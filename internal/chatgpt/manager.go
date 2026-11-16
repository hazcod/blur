// Package chatgpt implements the documented public-client Sign in with ChatGPT
// flow. Account connection does not select a model or admit remote evidence.
package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/platform"
)

const (
	issuer          = "https://auth.openai.com"
	resource        = "https://api.openai.com/v1"
	vaultName       = "chatgpt.accounts.v1"
	maxVaultBytes   = 256 << 10
	maxAccountBytes = 32 << 10
	maxAccounts     = 16
	requestTimeout  = 45 * time.Second
)

var (
	ErrConfig      = errors.New("chatgpt: invalid configuration")
	ErrUnavailable = errors.New("chatgpt: service unavailable")
	ErrProtocol    = errors.New("chatgpt: invalid provider response")
	ErrIdentity    = errors.New("chatgpt: account identity validation failed")
	ErrPermission  = errors.New("chatgpt: ChatGPT plan permission required")
	ErrSignedOut   = errors.New("chatgpt: account must sign in")
	ErrCancelled   = errors.New("chatgpt: sign in cancelled")
	ErrStorage     = errors.New("chatgpt: protected credential storage unavailable")
	ErrOutputLimit = errors.New("chatgpt: accepted output limit exceeded")
	ErrIncomplete  = errors.New("chatgpt: response did not complete")
)

// SecretStore is intentionally compatible with the platform OS secret store.
// Put must atomically replace a named value and copy the supplied bytes.
type SecretStore interface {
	Get(context.Context, string, int) ([]byte, error)
	Put(context.Context, string, []byte) error
	Delete(context.Context, string) error
}
type Account struct {
	ID               string `json:"id"`
	Issuer           string `json:"issuer"`
	Subject          string `json:"subject"`
	ClientID         string `json:"client_id"`
	Label            string `json:"label"`
	Connected        bool   `json:"connected"`
	InferenceEnabled bool   `json:"inference_enabled"`
}
type Model struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}
type credentials struct {
	AccessToken       string    `json:"access_token"`
	RefreshToken      string    `json:"refresh_token,omitempty"`
	IDToken           string    `json:"id_token,omitempty"`
	Scope             string    `json:"scope"`
	ExpiresAt         time.Time `json:"expires_at"`
	EarliestRefreshAt time.Time `json:"earliest_refresh_at,omitempty"`
}

func (credentials) String() string   { return "[protected ChatGPT credentials]" }
func (credentials) GoString() string { return "[protected ChatGPT credentials]" }

type accountRecord struct {
	Account Account      `json:"account"`
	Tokens  *credentials `json:"tokens,omitempty"`
}

func (accountRecord) String() string   { return "[protected ChatGPT registration]" }
func (accountRecord) GoString() string { return "[protected ChatGPT registration]" }

type accountVault struct {
	Version  int             `json:"version"`
	HostID   string          `json:"host_id"`
	Accounts []accountRecord `json:"accounts"`
}

func (accountVault) String() string   { return "[protected ChatGPT account vault]" }
func (accountVault) GoString() string { return "[protected ChatGPT account vault]" }

type accountState struct {
	gate   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
}
type Manager struct {
	store    SecretStore
	client   *http.Client
	now      func() time.Time
	mu       sync.Mutex
	statesMu sync.Mutex
	states   map[string]*accountState
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	blocked  map[string]bool
	epochs   map[string]uint64
}

func (*Manager) String() string   { return "[ChatGPT account manager]" }
func (*Manager) GoString() string { return "[ChatGPT account manager]" }
func NewManager(store SecretStore) (*Manager, error) {
	if store == nil {
		return nil, ErrConfig
	}
	ctx, cancel := context.WithCancel(context.Background())
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{Proxy: nil, DialContext: dialer.DialContext, DisableCompression: true, MaxIdleConns: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	return &Manager{store: store, client: &http.Client{Timeout: requestTimeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, now: time.Now, states: map[string]*accountState{}, blocked: map[string]bool{}, epochs: map[string]uint64{}, ctx: ctx, cancel: cancel}, nil
}
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.statesMu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel()
		for _, s := range m.states {
			s.cancel()
		}
	}
	m.statesMu.Unlock()
	m.CloseIdleConnections()
}
func (m *Manager) CloseIdleConnections() {
	if m != nil && m.client != nil {
		m.client.CloseIdleConnections()
	}
}
func (m *Manager) requestContext(parent context.Context, id string) (context.Context, func(), *accountState, error) {
	if m == nil || parent == nil {
		return nil, nil, nil, ErrConfig
	}
	if err := parent.Err(); err != nil {
		return nil, nil, nil, err
	}
	if !validAccountID(id) {
		return nil, nil, nil, ErrConfig
	}
	if _, err := m.account(parent, id); err != nil {
		return nil, nil, nil, err
	}
	m.statesMu.Lock()
	if m.closed {
		m.statesMu.Unlock()
		return nil, nil, nil, ErrUnavailable
	}
	if m.blocked[id] {
		m.statesMu.Unlock()
		return nil, nil, nil, ErrSignedOut
	}
	s := m.states[id]
	if s == nil {
		if len(m.states) >= maxAccounts {
			m.statesMu.Unlock()
			return nil, nil, nil, ErrUnavailable
		}
		c, cancel := context.WithCancel(m.ctx)
		s = &accountState{gate: make(chan struct{}, 1), ctx: c, cancel: cancel}
		m.states[id] = s
	}
	m.statesMu.Unlock()
	ctx, cancel := context.WithTimeout(parent, requestTimeout)
	stop := context.AfterFunc(s.ctx, cancel)
	return ctx, func() { stop(); cancel() }, s, nil
}
func (m *Manager) readVault(ctx context.Context) (accountVault, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadVault(ctx)
}
func (m *Manager) loadVault(ctx context.Context) (accountVault, error) {
	if err := ctx.Err(); err != nil {
		return accountVault{}, err
	}
	data, err := m.store.Get(ctx, vaultName, maxVaultBytes)
	if err != nil {
		var se *platform.SecretStoreError
		if errors.As(err, &se) && se.State == platform.SecretNotFound {
			return accountVault{Version: 1}, nil
		}
		return accountVault{}, ErrStorage
	}
	defer clear(data)
	var v accountVault
	if len(data) > maxVaultBytes || json.Unmarshal(data, &v) != nil || v.Version != 1 || len(v.Accounts) > maxAccounts || v.HostID == "" || !safeText(v.HostID, 128) {
		return accountVault{}, ErrStorage
	}
	seen := map[string]bool{}
	for _, r := range v.Accounts {
		b, _ := json.Marshal(r)
		size := len(b)
		clear(b)
		a := r.Account
		if size > maxAccountBytes || a.ID != accountID(a.Subject, a.ClientID) || a.Issuer != issuer || !validClientID(a.ClientID) || !safeText(a.Subject, 512) || !safeText(a.Label, 256) || seen[a.ID] {
			return accountVault{}, ErrStorage
		}
		seen[a.ID] = true
		if r.Tokens != nil && (!validToken(r.Tokens.AccessToken) || r.Tokens.RefreshToken != "" && !validToken(r.Tokens.RefreshToken) || r.Tokens.IDToken != "" && !validToken(r.Tokens.IDToken) || !safeText(r.Tokens.Scope, 2048) || r.Tokens.ExpiresAt.IsZero()) {
			return accountVault{}, ErrStorage
		}
	}
	return v, nil
}
func (m *Manager) saveVault(ctx context.Context, v accountVault) error {
	if len(v.Accounts) > maxAccounts {
		return ErrStorage
	}
	for _, record := range v.Accounts {
		b, err := json.Marshal(record)
		size := len(b)
		clear(b)
		if err != nil || size > maxAccountBytes {
			return ErrStorage
		}
	}
	data, err := json.Marshal(v)
	if err != nil || len(data) > maxVaultBytes {
		clear(data)
		return ErrStorage
	}
	defer clear(data)
	if err := m.store.Put(ctx, vaultName, data); err != nil {
		return ErrStorage
	}
	return nil
}
func (m *Manager) updateVault(ctx context.Context, update func(*accountVault) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, err := m.loadVault(ctx)
	if err != nil {
		return err
	}
	if err := update(&v); err != nil {
		return err
	}
	return m.saveVault(ctx, v)
}
func (m *Manager) Accounts(ctx context.Context) ([]Account, error) {
	if m == nil || ctx == nil {
		return nil, ErrConfig
	}
	v, err := m.readVault(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(v.Accounts))
	for _, r := range v.Accounts {
		out = append(out, publicAccount(r))
	}
	return out, nil
}
func publicAccount(r accountRecord) Account {
	a := r.Account
	a.Connected = r.Tokens != nil && r.Tokens.AccessToken != ""
	a.InferenceEnabled = a.Connected && hasScope(r.Tokens.Scope, "chatgpt.tokens.use.direct") && hasScope(r.Tokens.Scope, "resource.invoke")
	return a
}
func (m *Manager) account(ctx context.Context, id string) (accountRecord, error) {
	v, err := m.readVault(ctx)
	if err != nil {
		return accountRecord{}, err
	}
	for _, r := range v.Accounts {
		if r.Account.ID == id {
			return r, nil
		}
	}
	return accountRecord{}, ErrSignedOut
}
func accountID(subject, client string) string {
	sum := sha256.Sum256([]byte(issuer + "\x00" + subject + "\x00" + client))
	return hex.EncodeToString(sum[:])
}
func safeText(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validClientID(s string) bool {
	return s != "" && s != "dynamic_agent_client" && safeText(s, 256) && strings.TrimSpace(s) == s && !strings.ContainsAny(s, " &?/#")
}
func validToken(s string) bool { return s != "" && safeText(s, 16<<10) && strings.TrimSpace(s) == s }
func hasScope(scopes, scope string) bool {
	for _, s := range strings.Fields(scopes) {
		if s == scope {
			return true
		}
	}
	return false
}
func randomValue(size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", ErrUnavailable
	}
	defer clear(value)
	return hex.EncodeToString(value), nil
}
func newHostID() (string, error) {
	raw := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", ErrUnavailable
	}
	raw[6] = (raw[6] & 15) | 64
	raw[8] = (raw[8] & 63) | 128
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:]), nil
}

func (m *Manager) block(id string) {
	m.statesMu.Lock()
	m.blocked[id] = true
	m.epochs[id]++
	if s := m.states[id]; s != nil {
		s.cancel()
		delete(m.states, id)
	}
	m.statesMu.Unlock()
}

func validAccountID(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 32 && strings.ToLower(id) == id
}
