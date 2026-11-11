package model

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxCompatibleOutputTokens = 384
	maxCompatibleRequestBytes = 32 << 10
	maxCompatibleResponse     = 64 << 10
	compatibleTimeout         = 45 * time.Second
)

var (
	ErrCompatibleConfig      = errors.New("model: invalid OpenAI-compatible configuration")
	ErrCompatibleUnavailable = errors.New("model: OpenAI-compatible provider unavailable")
	ErrCompatibleResponse    = errors.New("model: invalid OpenAI-compatible response")
)

// OpenAICompatible implements the common chat-completions shape used by many
// local and hosted providers. Remote endpoints require HTTPS and credentials;
// loopback endpoints bypass proxies, pin resolution to loopback, and need no
// credential. Redirects are never followed.
type OpenAICompatible struct {
	endpoint string
	model    string
	apiKey   string
	client   *http.Client
}

type compatibleRequest struct {
	Model       string              `json:"model"`
	Messages    []compatibleMessage `json:"messages"`
	Temperature float32             `json:"temperature"`
	MaxTokens   int                 `json:"max_tokens"`
	Stream      bool                `json:"stream"`
}

type compatibleMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type compatibleResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func NewOpenAICompatible(endpoint, modelName, apiKey string) (*OpenAICompatible, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.Host == "" || parsed.RawPath != "" || len(endpoint) > 1024 || len(modelName) == 0 || len(modelName) > 128 || !utf8.ValidString(modelName) || strings.ContainsAny(modelName, "\x00\r\n") || !validCompatibleCredential(apiKey) {
		return nil, ErrCompatibleConfig
	}
	local := loopbackHost(parsed.Hostname())
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && local) {
		return nil, ErrCompatibleConfig
	}
	if !local && apiKey == "" {
		return nil, ErrCompatibleConfig
	}
	if strings.Contains(parsed.Path, "\\") || strings.Contains(parsed.Path, "..") || parsed.Path != "" && path.Clean(parsed.Path) != parsed.Path {
		return nil, ErrCompatibleConfig
	}
	if port := parsed.Port(); port != "" {
		if _, err := net.LookupPort("tcp", port); err != nil {
			return nil, ErrCompatibleConfig
		}
	}
	dialer := &net.Dialer{Timeout: compatibleTimeout}
	transport := &http.Transport{
		Proxy: nil, DisableCompression: true, MaxIdleConns: 2, MaxIdleConnsPerHost: 1,
		IdleConnTimeout: 30 * time.Second, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	if local {
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialLoopback(ctx, network, address, net.DefaultResolver, dialer.DialContext)
		}
	} else {
		transport.DialContext = dialer.DialContext
	}
	return &OpenAICompatible{
		endpoint: strings.TrimRight(parsed.String(), "/"), model: modelName, apiKey: apiKey,
		client: &http.Client{Timeout: compatibleTimeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func validCompatibleCredential(value string) bool {
	return len(value) <= 4096 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func (m *OpenAICompatible) CloseIdleConnections() {
	if m != nil && m.client != nil {
		m.client.CloseIdleConnections()
	}
}

// CountInputTokens uses UTF-8 bytes as a conservative provider-independent
// upper bound. Providers with their own tokenizer can replace this counter.
func (m *OpenAICompatible) CountInputTokens(ctx context.Context, system, user string) (int, error) {
	if m == nil || m.client == nil || ctx == nil || !utf8.ValidString(system) || !utf8.ValidString(user) || !compatibleInputWithinPreflightLimit(len(system), len(user)) {
		return 0, ErrCompatibleConfig
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return len(system) + len(user), nil
}

func compatibleInputWithinPreflightLimit(systemBytes, userBytes int) bool {
	if systemBytes < 0 || userBytes < 0 || systemBytes > maxCompatibleRequestBytes {
		return false
	}
	return userBytes <= maxCompatibleRequestBytes-systemBytes
}

// Generate sends one bounded, non-streaming request and returns the sole
// choice's content. Callers must validate its domain-specific structure.
func (m *OpenAICompatible) Generate(ctx context.Context, system, user string, maxOutputTokens int) (string, error) {
	if m == nil || m.client == nil || ctx == nil || ctx.Err() != nil || len(system) == 0 || len(user) == 0 || !utf8.ValidString(system) || !utf8.ValidString(user) || !compatibleInputWithinPreflightLimit(len(system), len(user)) || maxOutputTokens < 1 || maxOutputTokens > MaxCompatibleOutputTokens {
		if ctx != nil && ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrCompatibleConfig
	}
	request := compatibleRequest{Model: m.model, Temperature: 0, MaxTokens: maxOutputTokens, Stream: false, Messages: []compatibleMessage{{Role: "system", Content: system}, {Role: "user", Content: user}}}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > maxCompatibleRequestBytes {
		clear(encoded)
		return "", ErrCompatibleConfig
	}
	defer clear(encoded)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return "", ErrCompatibleConfig
	}
	req.Header.Set("Content-Type", "application/json")
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}
	response, err := m.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrCompatibleUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", ErrCompatibleUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCompatibleResponse+1))
	if err != nil || len(body) > maxCompatibleResponse {
		clear(body)
		return "", ErrCompatibleUnavailable
	}
	defer clear(body)
	var result compatibleResponse
	if err := json.Unmarshal(body, &result); err != nil || len(result.Choices) != 1 {
		return "", ErrCompatibleResponse
	}
	content := result.Choices[0].Message.Content
	if len(content) == 0 || len(content) > MaxGeneratedContentBytes || !utf8.ValidString(content) {
		return "", ErrCompatibleResponse
	}
	return content, nil
}
