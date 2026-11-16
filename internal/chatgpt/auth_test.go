package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/hazcod/blur/internal/platform"
)

type memorySecrets struct {
	mu      sync.Mutex
	values  map[string][]byte
	failPut bool
}

func (s *memorySecrets) Get(_ context.Context, name string, limit int) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[name]
	if !ok {
		return nil, &platform.SecretStoreError{State: platform.SecretNotFound}
	}
	if len(v) > limit {
		return nil, errors.New("large secret")
	}
	return append([]byte(nil), v...), nil
}
func (s *memorySecrets) Put(_ context.Context, name string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failPut {
		return errors.New("PRIVATE credential storage failure")
	}
	if s.values == nil {
		s.values = map[string][]byte{}
	}
	s.values[name] = append([]byte(nil), v...)
	return nil
}
func (s *memorySecrets) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, name)
	return nil
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResponse(value any) *http.Response {
	b, _ := json.Marshal(value)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b)))}
}

type authFixture struct {
	t          *testing.T
	manager    *Manager
	secrets    *memorySecrets
	key        *rsa.PrivateKey
	now        time.Time
	nonce      string
	clientID   string
	tokenCalls atomic.Int32
	modelCalls atomic.Int32
	token      func(*http.Request) (*http.Response, error)
	discovery  map[string]any
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &authFixture{t: t, key: key, now: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC), secrets: &memorySecrets{}, clientID: "oaiapp_fixture", discovery: map[string]any{"issuer": "https://auth.openai.com", "authorization_endpoint": "https://auth.openai.com/api/accounts/authorize", "token_endpoint": "https://auth.openai.com/api/accounts/oauth/token", "jwks_uri": "https://auth.openai.com/.well-known/jwks.json", "revocation_endpoint": "https://auth.openai.com/oauth/revoke"}}
	f.manager, err = NewManager(f.secrets)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.now = func() time.Time { return f.now }
	t.Cleanup(f.manager.Close)
	f.manager.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://auth.openai.com/.well-known/openid-configuration":
			return jsonResponse(f.discovery), nil
		case "https://auth.openai.com/.well-known/jwks.json":
			return jsonResponse(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "fixture", Algorithm: "RS256", Use: "sig"}}}), nil
		case "https://auth.openai.com/api/accounts/oauth/token":
			f.tokenCalls.Add(1)
			if f.token != nil {
				return f.token(r)
			}
			_ = r.ParseForm()
			if r.Form.Get("client_id") != f.clientID || r.Form.Get("resource") != "https://api.openai.com/v1" || r.Form.Get("client_secret") != "" || r.Form.Get("code_verifier") == "" {
				t.Errorf("wrong public-client exchange")
			}
			return jsonResponse(map[string]any{"access_token": "PRIVATE_ACCESS", "refresh_token": "PRIVATE_REFRESH", "id_token": f.jwt(nil), "token_type": "Bearer", "expires_in": 3600, "scope": "openid resource.invoke chatgpt.tokens.use.direct offline_access"}), nil
		case "https://api.openai.com/v1/models":
			f.modelCalls.Add(1)
			return jsonResponse(map[string]any{"models": []any{map[string]string{"slug": "fixture-model", "display_name": "Fixture model", "visibility": "list"}, map[string]string{"slug": "hidden", "display_name": "Hidden", "visibility": "hide"}}}), nil
		case "https://auth.openai.com/oauth/revoke":
			return jsonResponse(map[string]any{}), nil
		default:
			t.Errorf("unexpected remote request %s", r.URL)
			return nil, errors.New("unexpected endpoint")
		}
	})
	return f
}
func (f *authFixture) jwt(change func(map[string]any)) string {
	f.t.Helper()
	claims := map[string]any{"iss": "https://auth.openai.com", "sub": "fixture-subject", "aud": []string{f.clientID}, "exp": f.now.Add(time.Hour).Unix(), "iat": f.now.Unix(), "nonce": f.nonce, "email": "same@example.test", "name": "Fixture"}
	if change != nil {
		change(claims)
	}
	b, _ := json.Marshal(claims)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithHeader("kid", "fixture"))
	if err != nil {
		f.t.Fatal(err)
	}
	signed, err := signer.Sign(b)
	if err != nil {
		f.t.Fatal(err)
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		f.t.Fatal(err)
	}
	return token
}
func (f *authFixture) login(saved string) Account {
	f.t.Helper()
	flow, err := f.manager.BeginSignIn(context.Background(), saved)
	if err != nil {
		f.t.Fatal(err)
	}
	u, _ := url.Parse(flow.AuthorizationURL())
	f.nonce = u.Query().Get("nonce")
	callback, _ := url.Parse(u.Query().Get("redirect_uri"))
	q := callback.Query()
	q.Set("state", u.Query().Get("state"))
	q.Set("code", "fixture-code")
	q.Set("client_id", f.clientID)
	callback.RawQuery = q.Encode()
	r, err := http.Get(callback.String())
	if err != nil {
		f.t.Fatal(err)
	}
	_ = r.Body.Close()
	account, err := flow.Wait(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return account
}

func TestSignInStartsLoopbackPKCEAndValidatesIdentityBeforeConnecting(t *testing.T) {
	f := newAuthFixture(t)
	flow, err := f.manager.BeginSignIn(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Cancel()
	u, _ := url.Parse(flow.AuthorizationURL())
	q := u.Query()
	f.nonce = q.Get("nonce")
	if q.Get("client_id") != "dynamic_agent_client" || q.Get("agent_name_hint") != "Blur" || q.Get("ext_agent_host_id") == "" || q.Get("code_challenge_method") != "S256" || len(q.Get("state")) < 32 || len(q.Get("nonce")) < 32 || len(q.Get("code_challenge")) != 43 {
		t.Fatal("missing public native OAuth controls")
	}
	callback, _ := url.Parse(q.Get("redirect_uri"))
	if callback.Scheme != "http" || callback.Hostname() != "127.0.0.1" || callback.Path != "/auth/callback" {
		t.Fatal("incorrect callback shape")
	}
	cb := callback.Query()
	cb.Set("code", "fixture-code")
	cb.Set("client_id", "oaiapp_fixture")
	cb.Set("state", "incorrect")
	callback.RawQuery = cb.Encode()
	response, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode == 200 || f.tokenCalls.Load() != 0 {
		t.Fatal("unmatched state consumed sign in")
	}
	cb.Set("state", q.Get("state"))
	callback.RawQuery = cb.Encode()
	response, err = http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	account, err := flow.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !account.Connected || !account.InferenceEnabled || account.Subject != "fixture-subject" || account.ClientID != "oaiapp_fixture" || account.Label != "same@example.test" || len(account.ID) != 64 {
		t.Fatalf("unverified account activated: %+v err=%v", account, err)
	}
	if f.modelCalls.Load() != 0 {
		t.Fatal("sign in triggered model work")
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", f.manager, flow), "PRIVATE_") {
		t.Fatal("secret material formatted")
	}
	accounts, err := f.manager.Accounts(context.Background())
	if err != nil || len(accounts) != 1 || accounts[0].ID != account.ID {
		t.Fatalf("accounts=%v err=%v", accounts, err)
	}
	again, err := f.manager.BeginSignIn(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Cancel()
	againURL, _ := url.Parse(again.AuthorizationURL())
	if againURL.Query().Get("client_id") != "oaiapp_fixture" || againURL.Query().Has("agent_name_hint") || againURL.Query().Get("ext_agent_host_id") != q.Get("ext_agent_host_id") {
		t.Fatal("registration or host identity was not retained")
	}
}

func TestSignInRejectsWrongIDTokenClaimsAndSignatures(t *testing.T) {
	for _, bad := range []string{"issuer", "audience", "expiry", "nonce", "signature"} {
		t.Run(bad, func(t *testing.T) {
			f := newAuthFixture(t)
			f.token = func(*http.Request) (*http.Response, error) {
				token := f.jwt(func(c map[string]any) {
					switch bad {
					case "issuer":
						c["iss"] = "https://evil.test"
					case "audience":
						c["aud"] = "other"
					case "expiry":
						c["exp"] = f.now.Add(-time.Minute).Unix()
					case "nonce":
						c["nonce"] = "other"
					}
				})
				if bad == "signature" {
					parts := strings.Split(token, ".")
					parts[2] = "bad"
					token = strings.Join(parts, ".")
				}
				return jsonResponse(map[string]any{"access_token": "PRIVATE_ACCESS", "id_token": token, "token_type": "Bearer", "expires_in": 3600, "scope": "chatgpt.tokens.use.direct"}), nil
			}
			flow, err := f.manager.BeginSignIn(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(flow.AuthorizationURL())
			f.nonce = u.Query().Get("nonce")
			cb, _ := url.Parse(u.Query().Get("redirect_uri"))
			cb.RawQuery = url.Values{"state": {u.Query().Get("state")}, "code": {"code"}, "client_id": {"oaiapp_fixture"}}.Encode()
			res, err := http.Get(cb.String())
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if account, err := flow.Wait(context.Background()); !errors.Is(err, ErrIdentity) || account.Connected {
				t.Fatalf("unverified token accepted: account=%+v err=%v", account, err)
			}
		})
	}
}
func TestSignInRejectsNonOfficialDiscoveredEndpoints(t *testing.T) {
	f := newAuthFixture(t)
	f.discovery["token_endpoint"] = "https://evil.test/token"
	if flow, err := f.manager.BeginSignIn(context.Background(), ""); !errors.Is(err, ErrProtocol) || flow != nil {
		t.Fatalf("untrusted endpoint accepted: %v", err)
	}
}
func TestIdentityOnlyAccountDoesNotPermitInference(t *testing.T) {
	f := newAuthFixture(t)
	f.token = func(*http.Request) (*http.Response, error) {
		return jsonResponse(map[string]any{"access_token": "PRIVATE_ACCESS", "id_token": f.jwt(nil), "token_type": "Bearer", "expires_in": 3600, "scope": "openid email"}), nil
	}
	a := f.login("")
	if !a.Connected || a.InferenceEnabled {
		t.Fatalf("identity scope enabled inference: %+v", a)
	}
	if _, err := f.manager.ListModels(context.Background(), a.ID); !errors.Is(err, ErrPermission) {
		t.Fatal("identity-only grant reached model API", err)
	}
}
func TestConcurrentRefreshRotatesOnlyOnceAndTemporaryFailureKeepsCredentials(t *testing.T) {
	f := newAuthFixture(t)
	a := f.login("")
	f.now = f.now.Add(2 * time.Hour)
	var refreshCalls atomic.Int32
	f.token = func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "PRIVATE_REFRESH" || r.Form.Has("scope") {
			t.Error("wrong rotating refresh grant")
		}
		refreshCalls.Add(1)
		return jsonResponse(map[string]any{"access_token": "ROTATED_ACCESS", "refresh_token": "ROTATED_REFRESH", "token_type": "Bearer", "expires_in": 3600}), nil
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			models, err := f.manager.ListModels(context.Background(), a.ID)
			if err != nil || len(models) != 1 || models[0].Slug != "fixture-model" {
				t.Errorf("models=%v err=%v", models, err)
			}
		})
	}
	wg.Wait()
	if refreshCalls.Load() != 1 {
		t.Fatalf("rotated refresh token raced: %d", refreshCalls.Load())
	}
	f.now = f.now.Add(2 * time.Hour)
	f.token = func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":"temporarily_unavailable"}`))}, nil
	}
	if _, err := f.manager.ListModels(context.Background(), a.ID); err == nil {
		t.Fatal("temporary failure succeeded")
	}
	accounts, err := f.manager.Accounts(context.Background())
	if err != nil || !accounts[0].Connected {
		t.Fatal("temporary failure deleted working account")
	}
}
func TestDisconnectRetainsRegistrationAndReportsUnconfirmedRevocation(t *testing.T) {
	f := newAuthFixture(t)
	a := f.login("")
	f.manager.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline PRIVATE_REFRESH") })
	confirmed, err := f.manager.Disconnect(context.Background(), a.ID)
	if confirmed || err == nil || strings.Contains(err.Error(), "PRIVATE_") {
		t.Fatal("revocation was incorrectly confirmed", err)
	}
	accounts, err := f.manager.Accounts(context.Background())
	if err != nil || len(accounts) != 1 || accounts[0].Connected || accounts[0].ClientID != a.ClientID {
		t.Fatal("local sign out lost registration", accounts, err)
	}
}

func TestRegistrationsWithSameEmailRemainDistinct(t *testing.T) {
	f := newAuthFixture(t)
	first := f.login("")
	f.clientID = "oaiapp_second"
	second := f.login("")
	accounts, err := f.manager.Accounts(context.Background())
	if err != nil || len(accounts) != 2 || first.ID == second.ID || first.Label != second.Label || first.ClientID == second.ClientID {
		t.Fatal("email collapsed separate registrations", accounts, err)
	}
}
func TestOAuthFormattingDoesNotExposeVerifierOrCredentials(t *testing.T) {
	f := newAuthFixture(t)
	flow, err := f.manager.BeginSignIn(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Cancel()
	for _, text := range []string{fmt.Sprintf("%+v %#v", flow, flow), fmt.Sprintf("%+v %#v", credentials{AccessToken: "PRIVATE_ACCESS", RefreshToken: "PRIVATE_REFRESH"}, credentials{AccessToken: "PRIVATE_ACCESS"})} {
		if strings.Contains(text, flow.secrets.verifier) || strings.Contains(text, "PRIVATE_") {
			t.Fatal("OAuth secret appeared in formatting")
		}
	}
}

func TestDisconnectPreventsPendingReauthorizationFromReconnecting(t *testing.T) {
	f := newAuthFixture(t)
	a := f.login("")
	flow, err := f.manager.BeginSignIn(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(flow.AuthorizationURL())
	f.nonce = u.Query().Get("nonce")
	entered, release := make(chan struct{}), make(chan struct{})
	f.token = func(*http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return jsonResponse(map[string]any{"access_token": "NEW_ACCESS", "refresh_token": "NEW_REFRESH", "id_token": f.jwt(nil), "token_type": "Bearer", "expires_in": 3600, "scope": "openid resource.invoke chatgpt.tokens.use.direct offline_access"}), nil
	}
	cb, _ := url.Parse(u.Query().Get("redirect_uri"))
	cb.RawQuery = url.Values{"state": {u.Query().Get("state")}, "code": {"code"}, "client_id": {"oaiapp_fixture"}}.Encode()
	res, err := http.Get(cb.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	done := make(chan error, 1)
	go func() { _, err := flow.Wait(context.Background()); done <- err }()
	<-entered
	if _, err := f.manager.Disconnect(context.Background(), a.ID); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrCancelled) && !errors.Is(err, context.Canceled) {
		t.Fatal("pending reauthorization undid disconnect", err)
	}
	accounts, err := f.manager.Accounts(context.Background())
	if err != nil || accounts[0].Connected {
		t.Fatal("disconnected account reactivated", accounts, err)
	}
}

func TestSignInAccountLimitRejectsBeforeOpeningAnotherTransaction(t *testing.T) {
	f := newAuthFixture(t)
	for i := 0; i < 16; i++ {
		f.clientID = fmt.Sprintf("oaiapp_fixture_%d", i)
		f.login("")
	}
	if flow, err := f.manager.BeginSignIn(context.Background(), ""); !errors.Is(err, ErrStorage) || flow != nil {
		t.Fatal("account bound allowed another authorization", err)
	}
}
func TestDiscoveryDoesNotFollowRedirects(t *testing.T) {
	f := newAuthFixture(t)
	var calls atomic.Int32
	f.manager.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://evil.test/credentials"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if flow, err := f.manager.BeginSignIn(context.Background(), ""); err == nil || flow != nil || calls.Load() != 1 {
		t.Fatal("discovery followed a redirect", err)
	}
}

func TestDisconnectProtectedWriteFailureDoesNotClaimLocalSignOut(t *testing.T) {
	f := newAuthFixture(t)
	a := f.login("")
	f.secrets.mu.Lock()
	f.secrets.failPut = true
	f.secrets.mu.Unlock()
	confirmed, err := f.manager.Disconnect(context.Background(), a.ID)
	if confirmed || !errors.Is(err, ErrStorage) || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("failed credential clearing claimed success", confirmed, err)
	}
	accounts, err := f.manager.Accounts(context.Background())
	if err != nil || len(accounts) != 1 || !accounts[0].Connected {
		t.Fatal("failed protected write pretended credentials were absent", accounts, err)
	}
}
