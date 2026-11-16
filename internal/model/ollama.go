// Package model contains provider implementations behind small model ports.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ContextTokenLimit        = 8192
	MaxOutputTokens          = 192  // default for bounded background derivations
	MaxCompletionTokens      = 1536 // hard ceiling for explicit answer requests
	PromptTemplateReserve    = 512
	MaxRequestBytes          = 32 << 10
	MaxResponseBytes         = 64 << 10
	MaxGeneratedContentBytes = 12 << 10
	ollamaTimeout            = 45 * time.Second
)

var (
	ErrOllamaConfig      = errors.New("model: invalid Ollama configuration")
	ErrOllamaUnavailable = errors.New("model: Ollama unavailable")
	ErrOllamaBudget      = errors.New("model: Ollama context budget exceeded")
	ErrOllamaResponse    = errors.New("model: invalid Ollama response")
)

type Request struct {
	System          string
	User            string
	Format          string
	Schema          json.RawMessage
	MaxOutputTokens int
	KeepAlive       time.Duration // explicit interactive residency; zero keeps provider default
}

type Response struct {
	Text         string
	InputTokens  int
	OutputTokens int
}

type Ollama struct {
	endpoint string
	model    string
	client   *http.Client
}

type ollamaRequest struct {
	Model     string          `json:"model"`
	Think     *bool           `json:"think,omitempty"`
	Messages  []ollamaMessage `json:"messages"`
	Stream    bool            `json:"stream"`
	KeepAlive string          `json:"keep_alive,omitempty"`
	Format    any             `json:"format,omitempty"`
	Options   ollamaOptions   `json:"options"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaOptions struct {
	Temperature float32 `json:"temperature"`
	NumPredict  int     `json:"num_predict"`
	NumCtx      int     `json:"num_ctx"`
}

type ollamaResponse struct {
	Model       string `json:"model"`
	RemoteHost  string `json:"remote_host"`
	RemoteModel string `json:"remote_model"`
	Message     struct {
		Content string `json:"content"`
	} `json:"message"`
	Done            bool `json:"done"`
	PromptEvalCount int  `json:"prompt_eval_count"`
	EvalCount       int  `json:"eval_count"`
}

// NewOllama accepts only explicit loopback HTTP endpoints. Proxy environment
// variables, redirects, and DNS rebinding are bypassed or rejected.
func NewOllama(endpoint, modelName string) (*Ollama, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.Host == "" || parsed.Path != "" && parsed.Path != "/" || !loopbackHost(parsed.Hostname()) || len(endpoint) > 512 || len(modelName) == 0 || len(modelName) > 128 || !utf8.ValidString(modelName) || strings.ContainsAny(modelName, "\x00\r\n") {
		return nil, ErrOllamaConfig
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, ErrOllamaConfig
		}
	}
	client := &http.Client{
		Timeout:       ollamaTimeout,
		Transport:     newOllamaTransport(net.DefaultResolver),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Ollama{endpoint: strings.TrimRight(parsed.String(), "/"), model: modelName, client: client}, nil
}

func (o *Ollama) CloseIdleConnections() {
	if o != nil && o.client != nil {
		o.client.CloseIdleConnections()
	}
}

// CountInputTokens reports a conservative byte-level upper bound for local
// UTF-8 tokenizers. Provider-specific tokenizers can replace this port later.
func (o *Ollama) CountInputTokens(ctx context.Context, system, user string) (int, error) {
	if o == nil || ctx == nil || ctx.Err() != nil || !utf8.ValidString(system) || !utf8.ValidString(user) {
		if ctx != nil && ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, ErrOllamaConfig
	}
	return len(system) + len(user), nil
}

func (o *Ollama) Generate(ctx context.Context, input Request) (Response, error) {
	if o == nil || o.client == nil || ctx == nil || input.MaxOutputTokens < 1 || input.MaxOutputTokens > MaxCompletionTokens || input.KeepAlive < 0 || input.KeepAlive > 10*time.Minute || input.Format != "" && input.Format != "json" || len(input.System) == 0 || len(input.User) == 0 || !utf8.ValidString(input.System) || !utf8.ValidString(input.User) {
		return Response{}, ErrOllamaConfig
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	var format any
	if input.Format != "" {
		format = input.Format
	}
	if len(input.Schema) > 0 {
		if input.Format != "" || len(input.Schema) > 4096 {
			return Response{}, ErrOllamaConfig
		}
		var schema map[string]json.RawMessage
		if json.Unmarshal(input.Schema, &schema) != nil || string(schema["type"]) != `"object"` {
			return Response{}, ErrOllamaConfig
		}
		copy := append(json.RawMessage(nil), input.Schema...)
		defer clear(copy)
		format = copy
	}
	inputUpperBound := len(input.System) + len(input.User) + len(input.Schema)
	if inputUpperBound+input.MaxOutputTokens+PromptTemplateReserve > ContextTokenLimit {
		return Response{}, ErrOllamaBudget
	}
	identity, err := o.ResolveCompletionModel(ctx)
	if err != nil {
		return Response{}, err
	}
	think := false
	keepAlive := ""
	if input.KeepAlive > 0 {
		keepAlive = input.KeepAlive.String()
	}
	request := ollamaRequest{
		Model:     o.model,
		Think:     &think,
		Messages:  []ollamaMessage{{Role: "system", Content: input.System}, {Role: "user", Content: input.User}},
		Stream:    false,
		KeepAlive: keepAlive,
		Format:    format,
		Options:   ollamaOptions{Temperature: 0, NumPredict: input.MaxOutputTokens, NumCtx: ollamaContextSize(inputUpperBound, input.MaxOutputTokens)},
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > MaxRequestBytes {
		clear(encoded)
		return Response{}, ErrOllamaBudget
	}
	defer clear(encoded)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint+"/api/chat", bytes.NewReader(encoded))
	if err != nil {
		return Response{}, ErrOllamaConfig
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := o.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, ErrOllamaUnavailable
	}
	defer func(close func() error) { _ = close() }(response.Body.Close)
	if response.StatusCode != http.StatusOK {
		return Response{}, ErrOllamaUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil || len(body) > MaxResponseBytes {
		clear(body)
		return Response{}, ErrOllamaUnavailable
	}
	defer clear(body)
	var result ollamaResponse
	if err := decodeOllamaResponse(body, &result); err != nil || !result.Done || len(result.Message.Content) == 0 || len(result.Message.Content) > MaxGeneratedContentBytes || !utf8.ValidString(result.Message.Content) || result.PromptEvalCount < 0 || result.PromptEvalCount > ContextTokenLimit || result.EvalCount < 0 || result.EvalCount > input.MaxOutputTokens {
		return Response{}, ErrOllamaResponse
	}
	if result.RemoteHost != "" || result.RemoteModel != "" {
		return Response{}, ErrOllamaRemoteModel
	}
	if result.Model != "" && canonicalOllamaModelName(result.Model) != identity.Name {
		return Response{}, ErrOllamaResponse
	}
	if err := o.VerifyLocalModel(ctx, identity); err != nil {
		return Response{}, err
	}
	return Response{Text: result.Message.Content, InputTokens: result.PromptEvalCount, OutputTokens: result.EvalCount}, nil
}

func ollamaContextSize(inputTokens, outputTokens int) int {
	needed := inputTokens + outputTokens + PromptTemplateReserve
	if needed < 1024 {
		needed = 1024
	}
	if needed >= ContextTokenLimit {
		return ContextTokenLimit
	}
	const quantum = 256
	return (needed + quantum - 1) / quantum * quantum
}

func decodeOllamaResponse(data []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(output); err != nil {
		return ErrOllamaResponse
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrOllamaResponse
	}
	return nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newOllamaTransport(resolver *net.Resolver) *http.Transport {
	dialer := &net.Dialer{Timeout: ollamaTimeout}
	return &http.Transport{
		Proxy: nil, DisableCompression: true, MaxIdleConns: 2, MaxIdleConnsPerHost: 1, IdleConnTimeout: 30 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialLoopback(ctx, network, address, resolver, dialer.DialContext)
		},
	}
}

func dialLoopback(ctx context.Context, network, address string, resolver *net.Resolver, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	if ctx == nil || resolver == nil || dial == nil {
		return nil, ErrOllamaConfig
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return nil, ErrOllamaConfig
	}
	var ips []net.IPAddr
	if literal := net.ParseIP(host); literal != nil {
		if !literal.IsLoopback() {
			return nil, ErrOllamaConfig
		}
		ips = []net.IPAddr{{IP: literal}}
	} else {
		if !strings.EqualFold(host, "localhost") {
			return nil, ErrOllamaConfig
		}
		ips, err = resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, ErrOllamaUnavailable
		}
	}
	if len(ips) == 0 {
		return nil, ErrOllamaUnavailable
	}
	for _, candidate := range ips {
		if candidate.IP == nil || !candidate.IP.IsLoopback() {
			return nil, ErrOllamaConfig
		}
	}
	var lastErr error
	for _, candidate := range ips {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conn, err := dial(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
