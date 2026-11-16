package chatgpt

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
)

const loginTimeout = 5 * time.Minute
const dynamicClient = "dynamic_agent_client"

type discovery struct {
	Issuer        string `json:"issuer"`
	Authorization string `json:"authorization_endpoint"`
	Token         string `json:"token_endpoint"`
	JWKS          string `json:"jwks_uri"`
	Revocation    string `json:"revocation_endpoint"`
}

func (m *Manager) discover(ctx context.Context) (discovery, error) {
	var d discovery
	if err := m.jsonRequest(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", "", nil, 64<<10, &d); err != nil {
		return d, err
	}
	if d.Issuer != issuer || d.Authorization != issuer+"/api/accounts/authorize" || d.Token != issuer+"/api/accounts/oauth/token" || d.JWKS != issuer+"/.well-known/jwks.json" || d.Revocation != "" && !officialAuthURL(d.Revocation) {
		return discovery{}, ErrProtocol
	}
	return d, nil
}
func officialAuthURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "auth.openai.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Opaque == "" && u.Path != "" && !strings.Contains(u.Path, "..")
}

type callback struct {
	code, client string
	err          error
}
type Login struct {
	manager    *Manager
	metadata   discovery
	selected   Account
	url        string
	redirect   string
	secrets    *loginSecrets
	ctx        context.Context
	cancel     context.CancelFunc
	server     *http.Server
	result     chan callback
	once       sync.Once
	waitMu     sync.Mutex
	waited     bool
	callbackMu sync.Mutex
	received   bool
	epoch      uint64
}

type loginSecrets struct{ nonce, verifier, state string }

func (loginSecrets) String() string   { return "[protected OAuth transaction]" }
func (loginSecrets) GoString() string { return "[protected OAuth transaction]" }

func (*Login) String() string   { return "[ChatGPT sign-in transaction]" }
func (*Login) GoString() string { return "[ChatGPT sign-in transaction]" }
func (l *Login) AuthorizationURL() string {
	if l == nil {
		return ""
	}
	return l.url
}
func (l *Login) Cancel() {
	if l != nil {
		l.once.Do(func() { l.cancel(); _ = l.server.Close() })
	}
}
func (m *Manager) BeginSignIn(parent context.Context, savedID string) (*Login, error) {
	if m == nil || parent == nil {
		return nil, ErrConfig
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if m.ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, loginTimeout)
	stop := context.AfterFunc(m.ctx, cancel)
	success := false
	defer func() {
		if !success {
			stop()
			cancel()
		}
	}()
	var selected Account
	var host string
	if err := m.updateVault(ctx, func(v *accountVault) error {
		if v.HostID == "" {
			var err error
			v.HostID, err = newHostID()
			if err != nil {
				return err
			}
		}
		host = v.HostID
		if savedID != "" {
			for _, r := range v.Accounts {
				if r.Account.ID == savedID {
					selected = r.Account
					return nil
				}
			}
			return ErrSignedOut
		}
		if len(v.Accounts) >= maxAccounts {
			return ErrStorage
		}
		return nil
	}); err != nil {
		return nil, err
	}
	m.statesMu.Lock()
	epoch := m.epochs[selected.ID]
	m.statesMu.Unlock()
	d, err := m.discover(ctx)
	if err != nil {
		return nil, err
	}
	state, err := randomValue(32)
	if err != nil {
		return nil, err
	}
	nonce, err := randomValue(32)
	if err != nil {
		return nil, err
	}
	verifier, err := randomValue(32)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, ErrUnavailable
	}
	redirect := "http://" + listener.Addr().String() + "/auth/callback"
	l := &Login{manager: m, metadata: d, selected: selected, epoch: epoch, redirect: redirect, secrets: &loginSecrets{nonce: nonce, verifier: verifier, state: state}, ctx: ctx, cancel: func() { stop(); cancel() }, result: make(chan callback, 1)}
	l.server = &http.Server{Handler: http.HandlerFunc(l.serveCallback), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 3 * time.Second, MaxHeaderBytes: 8 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	q := url.Values{"client_id": {dynamicClient}, "agent_name_hint": {"Blur"}, "ext_agent_host_id": {host}, "response_type": {"code"}, "redirect_uri": {redirect}, "scope": {"openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"}, "resource": {resource}, "state": {state}, "nonce": {nonce}, "code_challenge_method": {"S256"}}
	challenge := sha256.Sum256([]byte(verifier))
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	if selected.ClientID != "" {
		q.Set("client_id", selected.ClientID)
		q.Del("agent_name_hint")
	}
	l.url = d.Authorization + "?" + q.Encode()
	go func() { _ = l.server.Serve(listener) }()
	go func() { <-ctx.Done(); l.Cancel() }()
	success = true
	return l, nil
}
func (l *Login) serveCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	if r.Method != http.MethodGet || r.URL.Path != "/auth/callback" || len(r.URL.RawQuery) > 8192 || r.Host != strings.TrimSuffix(strings.TrimPrefix(l.redirect, "http://"), "/auth/callback") {
		http.Error(w, "Invalid sign-in callback.", 400)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, "Invalid sign-in callback.", 400)
		return
	}
	for _, key := range []string{"state", "code", "client_id", "error"} {
		if len(q[key]) > 1 {
			http.Error(w, "Invalid sign-in callback.", 400)
			return
		}
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(l.secrets.state)) != 1 {
		http.Error(w, "Invalid sign-in transaction.", 400)
		return
	}
	cb := callback{code: q.Get("code"), client: q.Get("client_id")}
	if q.Get("error") != "" {
		cb.err = ErrIdentity
		if q.Get("error") == "access_denied" {
			cb.err = ErrCancelled
		}
	} else if cb.code == "" || !safeText(cb.code, 2048) || strings.TrimSpace(cb.code) != cb.code {
		http.Error(w, "Invalid authorization code.", 400)
		return
	}
	if cb.err == nil {
		if l.selected.ClientID != "" {
			if cb.client != "" && cb.client != l.selected.ClientID {
				http.Error(w, "Registration did not match.", 400)
				return
			}
			cb.client = l.selected.ClientID
		}
		if !validClientID(cb.client) {
			http.Error(w, "Missing client registration.", 400)
			return
		}
	}
	l.callbackMu.Lock()
	defer l.callbackMu.Unlock()
	if l.received {
		http.Error(w, "Sign-in callback already received.", http.StatusConflict)
		return
	}
	select {
	case l.result <- cb:
		l.received = true
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "You can return to Blur.")
	default:
		http.Error(w, "Sign-in callback already received.", http.StatusConflict)
	}
}
func (l *Login) Wait(parent context.Context) (Account, error) {
	if l == nil || parent == nil {
		return Account{}, ErrConfig
	}
	l.waitMu.Lock()
	if l.waited {
		l.waitMu.Unlock()
		return Account{}, ErrCancelled
	}
	l.waited = true
	l.waitMu.Unlock()
	defer l.Cancel()
	var cb callback
	select {
	case <-parent.Done():
		return Account{}, parent.Err()
	case <-l.ctx.Done():
		return Account{}, l.ctx.Err()
	case cb = <-l.result:
	}
	if cb.err != nil {
		return Account{}, cb.err
	}
	ctx, cancel := context.WithTimeout(l.ctx, requestTimeout)
	stop := context.AfterFunc(parent, cancel)
	defer stop()
	defer cancel()
	// Persist the issued registration before exchange so invalid_grant does not
	// force a second dynamic registration. Pending registrations cannot infer.
	pending := Account{ID: accountID("", cb.client), Issuer: issuer, ClientID: cb.client, Label: "ChatGPT"}
	guardID, epoch := l.selected.ID, l.epoch
	if guardID == "" {
		guardID = pending.ID
		l.manager.statesMu.Lock()
		epoch = l.manager.epochs[guardID]
		l.manager.statesMu.Unlock()
	}
	if l.selected.Subject == "" {
		if err := l.manager.updateVault(ctx, func(v *accountVault) error {
			for _, r := range v.Accounts {
				if r.Account.ClientID == cb.client {
					return nil
				}
			}
			v.Accounts = append(v.Accounts, accountRecord{Account: pending})
			return nil
		}); err != nil {
			return Account{}, err
		}
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {cb.client}, "code": {cb.code}, "code_verifier": {l.secrets.verifier}, "redirect_uri": {l.redirect}, "resource": {resource}}
	tokens, err := l.manager.exchange(ctx, l.metadata.Token, form, nil)
	if err != nil {
		return Account{}, err
	}
	verified, label, err := l.manager.verifyID(ctx, l.metadata, tokens.IDToken, cb.client, l.secrets.nonce, tokens.AccessToken)
	if err != nil {
		return Account{}, err
	}
	if l.selected.Subject != "" && l.selected.Subject != verified {
		return Account{}, ErrIdentity
	}
	a := Account{ID: accountID(verified, cb.client), Issuer: issuer, Subject: verified, ClientID: cb.client, Label: label}
	record := accountRecord{Account: a, Tokens: &tokens}
	if err := l.manager.finishSignIn(ctx, guardID, epoch, pending.ID, record); err != nil {
		return Account{}, err
	}
	return publicAccount(record), nil
}
func (m *Manager) verifyID(ctx context.Context, d discovery, raw, client, nonce, access string) (string, string, error) {
	if !validToken(raw) {
		return "", "", ErrIdentity
	}
	var keys jose.JSONWebKeySet
	if err := m.jsonRequest(ctx, http.MethodGet, d.JWKS, "", nil, 128<<10, &keys); err != nil {
		return "", "", err
	}
	if len(keys.Keys) < 1 || len(keys.Keys) > 32 {
		return "", "", ErrIdentity
	}
	// The maintained verifier handles supported algorithms, signatures, exact
	// issuer/audience, expiry and nbf. Nonce and selected subject bind this flow.
	keySet := &verifiedKeys{keys: keys}
	verifier := oidc.NewVerifier(issuer, keySet, &oidc.Config{ClientID: client, SupportedSigningAlgs: []string{"RS256", "ES256"}, Now: m.now})
	token, err := verifier.Verify(ctx, raw)
	if err != nil || !safeText(token.Subject, 512) || token.Subject == "" {
		return "", "", ErrIdentity
	}
	if nonce != "" && subtle.ConstantTimeCompare([]byte(token.Nonce), []byte(nonce)) != 1 {
		return "", "", ErrIdentity
	}
	var claims struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		AZP   string `json:"azp"`
	}
	if token.Claims(&claims) != nil || claims.AZP != "" && claims.AZP != client || len(token.Audience) > 1 && claims.AZP != client {
		return "", "", ErrIdentity
	}
	if token.AccessTokenHash != "" && token.VerifyAccessToken(access) != nil {
		return "", "", ErrIdentity
	}
	label := "ChatGPT"
	if claims.Email != "" && safeText(claims.Email, 256) {
		label = claims.Email
	} else if claims.Name != "" && safeText(claims.Name, 256) {
		label = claims.Name
	}
	return token.Subject, label, nil
}

type verifiedKeys struct{ keys jose.JSONWebKeySet }

func (k *verifiedKeys) VerifySignature(ctx context.Context, raw string) ([]byte, error) {
	signed, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil || len(signed.Signatures) != 1 {
		return nil, ErrIdentity
	}
	header := signed.Signatures[0].Header
	matches := k.keys.Key(header.KeyID)
	if header.KeyID == "" || len(matches) != 1 {
		return nil, ErrIdentity
	}
	key := matches[0]
	if key.Use != "" && key.Use != "sig" || key.Algorithm != "" && key.Algorithm != header.Algorithm || !key.IsPublic() {
		return nil, ErrIdentity
	}
	return signed.Verify(key.Key)
}

func (m *Manager) finishSignIn(ctx context.Context, guardID string, epoch uint64, pendingID string, record accountRecord) error {
	m.statesMu.Lock()
	defer m.statesMu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if m.closed || m.epochs[guardID] != epoch {
		return ErrCancelled
	}
	if err := m.updateVault(ctx, func(v *accountVault) error {
		kept := v.Accounts[:0]
		for _, r := range v.Accounts {
			if r.Account.ID != pendingID && r.Account.ID != record.Account.ID {
				kept = append(kept, r)
			}
		}
		v.Accounts = append(kept, record)
		return nil
	}); err != nil {
		return err
	}
	m.epochs[guardID]++
	if guardID != record.Account.ID {
		m.epochs[record.Account.ID]++
	}
	delete(m.blocked, guardID)
	delete(m.blocked, record.Account.ID)
	if state := m.states[record.Account.ID]; state != nil {
		state.cancel()
		delete(m.states, record.Account.ID)
	}
	return nil
}
