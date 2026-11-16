package chatgpt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// APIError exposes bounded machine metadata only. Provider prose and private
// request/response bodies are deliberately excluded from formatting.
type APIError struct {
	Status    int
	Code      string
	Param     string
	RequestID string
}

func (e *APIError) Error() string {
	return "chatgpt: provider request failed (" + strconv.Itoa(e.Status) + ", " + e.Code + ")"
}
func (e *APIError) Unwrap() error { return ErrUnavailable }
func safeCode(code string) string {
	switch code {
	case "invalid_grant", "invalid_client", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused", "temporarily_unavailable", "subscription_sharing_user_not_eligible", "subscription_sharing_usage_limit_exceeded", "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable", "subscription_sharing_unsupported_capability", "subscription_sharing_route_not_supported", "subscription_sharing_invalid_user", "chatpass_v2_scope_not_authorized", "chatpass_v2_invalid_authorization_context", "rate_limit_exceeded", "server_error", "insufficient_quota", "context_length_exceeded":
		return code
	}
	return "unknown_error"
}
func tokenInvalid(err error) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	switch e.Code {
	case "invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused":
		return true
	}
	return false
}
func (m *Manager) jsonRequest(ctx context.Context, method, endpoint, access string, form url.Values, limit int, result any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return ErrConfig
	}
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if access != "" {
		req.Header.Set("Authorization", "Bearer "+access)
	}
	response, err := m.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	defer func(close func() error) { _ = close() }(response.Body.Close)
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	defer clear(data)
	if err != nil || len(data) > limit {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrProtocol
	}
	if response.StatusCode != http.StatusOK {
		return decodeAPIError(response, data)
	}
	if result == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(result) != nil {
		return ErrProtocol
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrProtocol
	}
	return nil
}
func decodeAPIError(response *http.Response, data []byte) error {
	var wire struct {
		Error json.RawMessage `json:"error"`
		Code  string          `json:"code"`
		Param string          `json:"param"`
	}
	_ = json.Unmarshal(data, &wire)
	code, param := wire.Code, wire.Param
	var text string
	if json.Unmarshal(wire.Error, &text) == nil {
		code = text
	} else {
		var detail struct {
			Code  string `json:"code"`
			Param string `json:"param"`
		}
		if json.Unmarshal(wire.Error, &detail) == nil {
			code, param = detail.Code, detail.Param
		}
	}
	if !safeText(param, 96) || strings.ContainsAny(param, " \t") {
		param = ""
	}
	requestID := response.Header.Get("x-request-id")
	if !safeText(requestID, 128) {
		requestID = ""
	}
	return &APIError{Status: response.StatusCode, Code: safeCode(code), Param: param, RequestID: requestID}
}

type tokenResponse struct {
	Access   string          `json:"access_token"`
	Refresh  string          `json:"refresh_token"`
	ID       string          `json:"id_token"`
	Type     string          `json:"token_type"`
	Expires  int             `json:"expires_in"`
	Scope    string          `json:"scope"`
	Earliest json.RawMessage `json:"earliest_refresh_at"`
}

func (m *Manager) exchange(ctx context.Context, endpoint string, form url.Values, old *credentials) (credentials, error) {
	var wire tokenResponse
	if err := m.jsonRequest(ctx, http.MethodPost, endpoint, "", form, maxAccountBytes, &wire); err != nil {
		return credentials{}, err
	}
	if !validToken(wire.Access) || !strings.EqualFold(wire.Type, "Bearer") || wire.Expires < 1 || wire.Expires > 86400 || !safeText(wire.Scope, 2048) || wire.Refresh != "" && !validToken(wire.Refresh) || wire.ID != "" && !validToken(wire.ID) {
		return credentials{}, ErrProtocol
	}
	c := credentials{AccessToken: wire.Access, RefreshToken: wire.Refresh, IDToken: wire.ID, Scope: wire.Scope, ExpiresAt: m.now().Add(time.Duration(wire.Expires) * time.Second)}
	if old != nil {
		if c.RefreshToken == "" {
			return credentials{}, ErrProtocol
		}
		if c.Scope == "" {
			c.Scope = old.Scope
		}
		if c.IDToken == "" {
			c.IDToken = old.IDToken
		}
	}
	if len(wire.Earliest) > 0 && string(wire.Earliest) != "null" {
		var stamp int64
		var text string
		if json.Unmarshal(wire.Earliest, &stamp) == nil {
			c.EarliestRefreshAt = time.Unix(stamp, 0)
		} else if json.Unmarshal(wire.Earliest, &text) == nil {
			var err error
			c.EarliestRefreshAt, err = time.Parse(time.RFC3339, text)
			if err != nil {
				return credentials{}, ErrProtocol
			}
		} else {
			return credentials{}, ErrProtocol
		}
		if c.EarliestRefreshAt.After(c.ExpiresAt) {
			return credentials{}, ErrProtocol
		}
	}
	return c, nil
}
func (m *Manager) accessToken(ctx context.Context, id string, state *accountState) (string, error) {
	select {
	case state.gate <- struct{}{}:
		defer func() { <-state.gate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	record, err := m.account(ctx, id)
	if err != nil {
		return "", err
	}
	account := publicAccount(record)
	if !account.Connected {
		return "", ErrSignedOut
	}
	if !account.InferenceEnabled {
		return "", ErrPermission
	}
	old := record.Tokens
	if old.ExpiresAt.After(m.now().Add(30 * time.Second)) {
		return old.AccessToken, nil
	}
	if m.now().Before(old.EarliestRefreshAt) {
		if m.now().Before(old.ExpiresAt) {
			return old.AccessToken, nil
		}
		return "", ErrSignedOut
	}
	if old.RefreshToken == "" {
		return "", ErrSignedOut
	}
	d, err := m.discover(ctx)
	if err != nil {
		return "", err
	}
	next, err := m.exchange(ctx, d.Token, url.Values{"grant_type": {"refresh_token"}, "client_id": {account.ClientID}, "refresh_token": {old.RefreshToken}, "resource": {resource}}, old)
	if err != nil {
		if tokenInvalid(err) {
			_ = m.clearCredentials(ctx, id)
		}
		return "", err
	}
	if next.IDToken != old.IDToken {
		subject, _, err := m.verifyID(ctx, d, next.IDToken, account.ClientID, "", next.AccessToken)
		if err != nil {
			return "", err
		}
		if subject != account.Subject {
			return "", ErrIdentity
		}
	}
	if err := m.updateVault(ctx, func(v *accountVault) error {
		for i := range v.Accounts {
			r := &v.Accounts[i]
			if r.Account.ID == id {
				if r.Tokens == nil || r.Tokens.RefreshToken != old.RefreshToken {
					return ErrSignedOut
				}
				r.Tokens = &next
				return nil
			}
		}
		return ErrSignedOut
	}); err != nil {
		return "", err
	}
	if !publicAccount(accountRecord{Account: account, Tokens: &next}).InferenceEnabled {
		return "", ErrPermission
	}
	return next.AccessToken, nil
}
func (m *Manager) ListModels(parent context.Context, id string) ([]Model, error) {
	ctx, done, state, err := m.requestContext(parent, id)
	if err != nil {
		return nil, err
	}
	defer done()
	access, err := m.accessToken(ctx, id, state)
	if err != nil {
		return nil, err
	}
	models, err := m.listModels(ctx, access)
	if state.ctx.Err() != nil {
		return nil, context.Canceled
	}
	return models, err
}
func (m *Manager) listModels(ctx context.Context, access string) ([]Model, error) {
	var wire struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}
	if err := m.jsonRequest(ctx, http.MethodGet, resource+"/models", access, nil, 128<<10, &wire); err != nil {
		return nil, err
	}
	if len(wire.Models) > 256 {
		return nil, ErrProtocol
	}
	out := make([]Model, 0, len(wire.Models))
	seen := map[string]bool{}
	for _, model := range wire.Models {
		if model.Visibility != "list" {
			continue
		}
		if !validModelSlug(model.Slug) || !safeText(model.DisplayName, 256) || model.DisplayName == "" || seen[model.Slug] {
			return nil, ErrProtocol
		}
		seen[model.Slug] = true
		out = append(out, Model{Slug: model.Slug, DisplayName: model.DisplayName})
	}
	return out, nil
}
func validModelSlug(s string) bool {
	return s != "" && safeText(s, 128) && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\t\r\n ")
}
func (m *Manager) clearCredentials(ctx context.Context, id string) error {
	return m.updateVault(ctx, func(v *accountVault) error {
		for i := range v.Accounts {
			if v.Accounts[i].Account.ID == id {
				v.Accounts[i].Tokens = nil
				return nil
			}
		}
		return ErrSignedOut
	})
}
func (m *Manager) Disconnect(parent context.Context, id string) (bool, error) {
	if m == nil || parent == nil {
		return false, ErrConfig
	}
	record, err := m.account(parent, id)
	if err != nil {
		return false, err
	}
	m.block(id)
	if record.Tokens == nil {
		// No retained token means remote revocation cannot be confirmed.
		return false, nil
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	confirmed := false
	if record.Tokens != nil && record.Tokens.RefreshToken != "" {
		d, discoveryErr := m.discover(ctx)
		if discoveryErr != nil {
			err = discoveryErr
		} else if d.Revocation == "" {
			err = ErrUnavailable
		} else {
			err = m.jsonRequest(ctx, http.MethodPost, d.Revocation, "", url.Values{"token": {record.Tokens.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {record.Account.ClientID}}, 64<<10, nil)
			confirmed = err == nil
		}
	}
	if !confirmed && err == nil {
		err = ErrUnavailable
	}
	clearCtx, clearCancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
	defer clearCancel()
	if clearErr := m.clearCredentials(clearCtx, id); clearErr != nil {
		return false, clearErr
	}
	return confirmed, err
}
