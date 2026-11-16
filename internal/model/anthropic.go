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
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	AnthropicEndpoint   = "https://api.anthropic.com"
	AnthropicAPIVersion = "2026-10-21"
	anthropicJSONHint   = "\nReturn only a valid JSON object."
)

var (
	ErrAnthropicConfig      = errors.New("model: invalid Claude API configuration")
	ErrAnthropicUnavailable = errors.New("model: Claude API unavailable")
	ErrAnthropicResponse    = errors.New("model: invalid Claude API response")
	ErrAnthropicBudget      = errors.New("model: Claude API context budget exceeded")
)

// Anthropic uses the native Claude Messages API at its fixed official origin.
// The caller owns remote consent. Counting is local and never sends a prompt;
// Generate makes one bounded request without tools, retries, or redirects.
// API shape: https://platform.claude.com/docs/en/api/messages/create
type Anthropic struct {
	model, apiKey, workspaceID string
	client                     *http.Client
}

func (*Anthropic) String() string   { return "[Claude completion provider]" }
func (*Anthropic) GoString() string { return "[Claude completion provider]" }

func NewAnthropic(modelName, apiKey, workspaceID string) (*Anthropic, error) {
	if !validAnthropicIdentifier(modelName) || !validAnthropicHeader(apiKey, 4096, false) || !validAnthropicHeader(workspaceID, 128, true) {
		return nil, ErrAnthropicConfig
	}
	dialer := &net.Dialer{Timeout: compatibleTimeout}
	transport := &http.Transport{
		Proxy: nil, DisableCompression: true, MaxIdleConns: 2, MaxIdleConnsPerHost: 1,
		IdleConnTimeout: 30 * time.Second, DialContext: dialer.DialContext,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &Anthropic{model: modelName, apiKey: apiKey, workspaceID: workspaceID, client: &http.Client{
		Transport: transport, Timeout: compatibleTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func validAnthropicHeader(value string, maxBytes int, empty bool) bool {
	return (empty || value != "") && len(value) <= maxBytes && utf8.ValidString(value) && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validAnthropicIdentifier(value string) bool {
	if !validAnthropicHeader(value, 128, false) {
		return false
	}
	return strings.IndexFunc(value, unicode.IsSpace) < 0
}

func (m *Anthropic) CloseIdleConnections() {
	if m != nil && m.client != nil {
		m.client.CloseIdleConnections()
	}
}

func (m *Anthropic) Identity(ctx context.Context) (string, error) {
	if m == nil || m.client == nil || ctx == nil {
		return "", ErrAnthropicConfig
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "anthropic:" + m.model, nil
}

func (m *Anthropic) CountInputTokens(ctx context.Context, system, user string) (int, error) {
	return m.CountRequestTokens(ctx, Request{System: system, User: user})
}

// UTF-8 bytes conservatively bound tokenizer pieces. Include any schema or
// JSON guidance exactly as Generate does; reserve/capacity stays with callers.
func (m *Anthropic) CountRequestTokens(ctx context.Context, input Request) (int, error) {
	if m == nil || m.client == nil || ctx == nil {
		return 0, ErrAnthropicConfig
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !utf8.ValidString(input.System) || !utf8.ValidString(input.User) || strings.ContainsRune(input.System, 0) || strings.ContainsRune(input.User, 0) || !compatibleInputWithinPreflightLimit(len(input.System), len(input.User)) || input.Format != "" && input.Format != "json" || input.KeepAlive < 0 || input.KeepAlive > 10*time.Minute {
		return 0, ErrAnthropicConfig
	}
	schemaBytes := len(input.Schema)
	if schemaBytes > 0 {
		var schema map[string]json.RawMessage
		if schemaBytes > 4096 || !utf8.Valid(input.Schema) || json.Unmarshal(input.Schema, &schema) != nil || string(schema["type"]) != `"object"` {
			return 0, ErrAnthropicConfig
		}
	}
	count := len(input.System) + len(input.User) + schemaBytes
	if input.Format == "json" && schemaBytes == 0 {
		count += len(anthropicJSONHint)
	}
	if count > MaxRequestBytes {
		return 0, ErrAnthropicBudget
	}
	return count, nil
}

func (m *Anthropic) Generate(ctx context.Context, input Request) (Response, error) {
	count, err := m.CountRequestTokens(ctx, input)
	if err != nil {
		return Response{}, err
	}
	if input.System == "" || input.User == "" || input.MaxOutputTokens < 1 || input.MaxOutputTokens > MaxCompletionTokens {
		return Response{}, ErrAnthropicConfig
	}
	if count+input.MaxOutputTokens+PromptTemplateReserve > ContextTokenLimit {
		return Response{}, ErrAnthropicBudget
	}
	request := struct {
		Model        string              `json:"model"`
		System       string              `json:"system"`
		Messages     []map[string]string `json:"messages"`
		MaxTokens    int                 `json:"max_tokens"`
		Stream       bool                `json:"stream"`
		OutputConfig any                 `json:"output_config,omitempty"`
	}{Model: m.model, System: input.System, Messages: []map[string]string{{"role": "user", "content": input.User}}, MaxTokens: input.MaxOutputTokens}
	if len(input.Schema) > 0 {
		request.OutputConfig = map[string]any{"format": map[string]any{"type": "json_schema", "schema": input.Schema}}
	} else if input.Format == "json" {
		request.System += anthropicJSONHint
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > MaxRequestBytes {
		clear(encoded)
		return Response{}, ErrAnthropicBudget
	}
	defer clear(encoded)
	body, err := m.api(ctx, http.MethodPost, "/v1/messages", encoded, MaxResponseBytes)
	if err != nil {
		return Response{}, err
	}
	defer clear(body)
	var response struct {
		Type, Role, Model string
		StopReason        string `json:"stop_reason"`
		Content           []struct{ Type, Text string }
		Usage             struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		}
	}
	if !utf8.Valid(body) || json.Unmarshal(body, &response) != nil || response.Type != "message" || response.Role != "assistant" || !validAnthropicIdentifier(response.Model) || response.StopReason != "end_turn" || len(response.Content) == 0 || len(response.Content) > 16 || response.Usage.InputTokens < 0 || response.Usage.InputTokens > ContextTokenLimit || response.Usage.OutputTokens < 0 || response.Usage.OutputTokens > input.MaxOutputTokens {
		return Response{}, ErrAnthropicResponse
	}
	var answer strings.Builder
	for _, block := range response.Content {
		// Current models can think by default and some reject disabling it.
		// These blocks are never answer text or follow-up conversation context.
		if block.Type == "thinking" || block.Type == "redacted_thinking" {
			continue
		}
		if block.Type != "text" || len(block.Text) > MaxGeneratedContentBytes-answer.Len() || !utf8.ValidString(block.Text) || strings.ContainsRune(block.Text, 0) {
			return Response{}, ErrAnthropicResponse
		}
		answer.WriteString(block.Text)
	}
	if strings.TrimSpace(answer.String()) == "" {
		return Response{}, ErrAnthropicResponse
	}
	return Response{Text: answer.String(), InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens}, nil
}

func (m *Anthropic) api(ctx context.Context, method, path string, encoded []byte, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, AnthropicEndpoint+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, ErrAnthropicConfig
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", m.apiKey)
	req.Header.Set("anthropic-version", AnthropicAPIVersion)
	if m.workspaceID != "" {
		req.Header.Set("anthropic-workspace-id", m.workspaceID)
	}
	response, err := m.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrAnthropicUnavailable
	}
	defer func(close func() error) { _ = close() }(response.Body.Close)
	if response.StatusCode != http.StatusOK {
		return nil, ErrAnthropicUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if ctx.Err() != nil {
		clear(body)
		return nil, ctx.Err()
	}
	if err != nil || int64(len(body)) > maxBytes {
		clear(body)
		return nil, ErrAnthropicResponse
	}
	return body, nil
}

type ClaudeModel struct{ ID, Name string }

// DiscoverClaudeModels reads this account's catalog without inference, model
// downloads, or local credential discovery. API keys come from the caller.
func DiscoverClaudeModels(ctx context.Context, apiKey, workspaceID string) ([]ClaudeModel, error) {
	client, err := NewAnthropic("discovery", apiKey, workspaceID)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	return client.ListModels(ctx)
}

// ListModels follows bounded account-catalog pagination at the official API.
// https://platform.claude.com/docs/en/api/models/list
func (m *Anthropic) ListModels(ctx context.Context) ([]ClaudeModel, error) {
	if _, err := m.Identity(ctx); err != nil {
		return nil, err
	}
	var result []ClaudeModel
	seen := make(map[string]bool)
	cursor := ""
	for page := 0; page < 8; page++ {
		query := url.Values{"limit": {"100"}}
		if cursor != "" {
			query.Set("after_id", cursor)
		}
		body, err := m.api(ctx, http.MethodGet, "/v1/models?"+query.Encode(), nil, 512<<10)
		if err != nil {
			return nil, err
		}
		var catalog struct {
			Data []struct {
				Type, ID    string
				DisplayName string `json:"display_name"`
				Lifecycle   string
			}
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		err = json.Unmarshal(body, &catalog)
		valid := utf8.Valid(body)
		clear(body)
		if !valid || err != nil || len(catalog.Data) > 100 || len(seen)+len(catalog.Data) > 512 {
			return nil, ErrAnthropicResponse
		}
		for _, item := range catalog.Data {
			if item.Type != "model" || !validAnthropicIdentifier(item.ID) || !validAnthropicHeader(item.DisplayName, 256, true) || seen[item.ID] {
				return nil, ErrAnthropicResponse
			}
			seen[item.ID] = true
			if item.Lifecycle == "retired" {
				continue
			}
			name := item.DisplayName
			if name == "" {
				name = item.ID
			}
			result = append(result, ClaudeModel{ID: item.ID, Name: name})
		}
		if !catalog.HasMore {
			return result, nil
		}
		if len(catalog.Data) == 0 || !validAnthropicIdentifier(catalog.LastID) || catalog.LastID != catalog.Data[len(catalog.Data)-1].ID {
			return nil, ErrAnthropicResponse
		}
		cursor = catalog.LastID
	}
	return nil, ErrAnthropicResponse
}

var _ LocalCompletion = (*Anthropic)(nil)
var _ RequestTokenCounter = (*Anthropic)(nil)
