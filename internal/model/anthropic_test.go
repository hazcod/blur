package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type anthropicRoundTrip func(*http.Request) (*http.Response, error)

func (f anthropicRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func anthropicReply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestAnthropicSendsNativeBoundedMessageToOfficialAPI(t *testing.T) {
	client, err := NewAnthropic("claude-test", "synthetic-api-key", "wrkspc_test")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	calls := 0
	client.client.Transport = anthropicRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost || r.URL.String() != "https://api.anthropic.com/v1/messages" || r.Header.Get("x-api-key") != "synthetic-api-key" || r.Header.Get("anthropic-version") != "2026-10-21" || r.Header.Get("anthropic-workspace-id") != "wrkspc_test" {
			t.Fatalf("incorrect Claude request target or headers: %s %s", r.Method, r.URL)
		}
		var body struct {
			Model        string                           `json:"model"`
			System       string                           `json:"system"`
			MaxTokens    int                              `json:"max_tokens"`
			Stream       bool                             `json:"stream"`
			Messages     []struct{ Role, Content string } `json:"messages"`
			OutputConfig struct {
				Format struct {
					Type   string
					Schema json.RawMessage
				}
			} `json:"output_config"`
			Temperature *float64 `json:"temperature"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "claude-test" || body.System != "Use supplied evidence." || body.MaxTokens != 128 || body.Stream || body.Temperature != nil || len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != "When is launch?" || body.OutputConfig.Format.Type != "json_schema" || !json.Valid(body.OutputConfig.Format.Schema) {
			t.Fatalf("invalid native message: %+v", body)
		}
		return anthropicReply(200, `{"id":"msg_test","type":"message","role":"assistant","model":"claude-test","stop_reason":"end_turn","content":[{"type":"text","text":"{\"answer\":\"Thursday\"}"}],"usage":{"input_tokens":22,"output_tokens":8}}`), nil
	})
	request := Request{System: "Use supplied evidence.", User: "When is launch?", Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`), MaxOutputTokens: 128}
	count, err := client.CountRequestTokens(context.Background(), request)
	if err != nil || count < len(request.System)+len(request.User)+len(request.Schema) || calls != 0 {
		t.Fatalf("token preflight disclosed content: count=%d calls=%d err=%v", count, calls, err)
	}
	got, err := client.Generate(context.Background(), request)
	if err != nil || calls != 1 || got.Text != `{"answer":"Thursday"}` || got.InputTokens != 22 || got.OutputTokens != 8 {
		t.Fatalf("completion=%+v calls=%d err=%v", got, calls, err)
	}
}

func TestAnthropicFormattingDoesNotExposeCredentialOrWorkspace(t *testing.T) {
	client, err := NewAnthropic("claude-test", "SYNTHETIC_PRIVATE_KEY", "wrkspc_PRIVATE_WORKSPACE")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	for _, formatted := range []string{fmt.Sprintf("%v", client), fmt.Sprintf("%+v", client), fmt.Sprintf("%#v", client)} {
		if strings.Contains(formatted, "SYNTHETIC_PRIVATE_KEY") || strings.Contains(formatted, "PRIVATE_WORKSPACE") {
			t.Fatal("provider formatting exposed protected configuration")
		}
	}
}

func TestAnthropicRejectsInvalidInputBeforeNetwork(t *testing.T) {
	for _, tc := range []struct{ model, key, workspace string }{
		{"", "key", ""}, {"claude-test", "", ""}, {"claude-test", "key\nother", ""}, {"claude-test", "key", "wrkspc_\r"},
	} {
		if _, err := NewAnthropic(tc.model, tc.key, tc.workspace); !errors.Is(err, ErrAnthropicConfig) {
			t.Fatalf("invalid config accepted: %v", err)
		}
	}
	client, _ := NewAnthropic("claude-test", "synthetic-key", "")
	calls := 0
	client.client.Transport = anthropicRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return anthropicReply(500, ""), nil })
	for _, request := range []Request{
		{System: "system", User: "user", MaxOutputTokens: 0},
		{System: "system", User: "user", MaxOutputTokens: MaxCompletionTokens + 1},
		{System: "system", User: strings.Repeat("x", MaxRequestBytes+1), MaxOutputTokens: 128},
		{System: "system", User: strings.Repeat("x", ContextTokenLimit), MaxOutputTokens: 128},
		{System: "system", User: "user", Schema: json.RawMessage(`[]`), MaxOutputTokens: 128},
		{System: "system", User: "user", Format: "xml", MaxOutputTokens: 128},
	} {
		if _, err := client.Generate(context.Background(), request); err == nil || calls != 0 {
			t.Fatalf("invalid request reached network: calls=%d err=%v", calls, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Generate(ctx, Request{System: "system", User: "user", MaxOutputTokens: 128}); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("canceled request reached network: calls=%d err=%v", calls, err)
	}
}

func TestAnthropicRejectsTruncatedToolAndOversizedResponses(t *testing.T) {
	client, _ := NewAnthropic("claude-test", "synthetic-key", "")
	for name, body := range map[string]string{
		"truncated":     `{"type":"message","role":"assistant","model":"claude-test","stop_reason":"max_tokens","content":[{"type":"text","text":"partial"}],"usage":{"input_tokens":4,"output_tokens":128}}`,
		"tool use":      `{"type":"message","role":"assistant","model":"claude-test","stop_reason":"end_turn","content":[{"type":"tool_use","name":"execute"}],"usage":{"input_tokens":4,"output_tokens":8}}`,
		"bad envelope":  `{"type":"error","content":[{"type":"text","text":"not an answer"}]}`,
		"trailing data": `{"type":"message"} {}`,
		"oversized":     strings.Repeat("x", MaxResponseBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			client.client.Transport = anthropicRoundTrip(func(*http.Request) (*http.Response, error) { return anthropicReply(200, body), nil })
			if _, err := client.Generate(context.Background(), Request{System: "system", User: "user", MaxOutputTokens: 128}); err == nil {
				t.Fatal("invalid response was accepted")
			}
		})
	}
}

func TestAnthropicRefusesRedirectsAndDoesNotExposeRemoteErrors(t *testing.T) {
	client, _ := NewAnthropic("claude-test", "synthetic-secret", "")
	calls := 0
	client.client.Transport = anthropicRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.anthropic.com" {
			t.Fatal("redirect received credentials")
		}
		response := anthropicReply(307, "synthetic-secret private remote detail")
		response.Header.Set("Location", "https://other.example/messages")
		return response, nil
	})
	_, err := client.Generate(context.Background(), Request{System: "system", User: "user", MaxOutputTokens: 128})
	if !errors.Is(err, ErrAnthropicUnavailable) || calls != 1 || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatalf("unsafe redirect/error handling: calls=%d err=%v", calls, err)
	}
}

func TestAnthropicListsAccountModelsWithBoundedPagination(t *testing.T) {
	client, _ := NewAnthropic("discovery", "synthetic-key", "wrkspc_test")
	calls := 0
	client.client.Transport = anthropicRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.Host != "api.anthropic.com" || r.URL.Path != "/v1/models" || r.URL.Query().Get("limit") != "100" || r.Header.Get("anthropic-workspace-id") != "wrkspc_test" {
			t.Fatalf("invalid model discovery request: %s %s", r.Method, r.URL)
		}
		if calls == 1 {
			return anthropicReply(200, `{"data":[{"type":"model","id":"claude-current","display_name":"Current Claude","lifecycle":"active"}],"has_more":true,"last_id":"claude-current"}`), nil
		}
		if calls != 2 || r.URL.Query().Get("after_id") != "claude-current" {
			t.Fatal("model cursor was not preserved")
		}
		return anthropicReply(200, `{"data":[{"type":"model","id":"claude-older","display_name":"Older Claude","lifecycle":"deprecated"},{"type":"model","id":"claude-retired","display_name":"Retired Claude","lifecycle":"retired"}],"has_more":false}`), nil
	})
	models, err := client.ListModels(context.Background())
	if err != nil || calls != 2 || len(models) != 2 || models[0].ID != "claude-current" || models[0].Name != "Current Claude" || models[1].ID != "claude-older" {
		t.Fatalf("account models=%+v calls=%d err=%v", models, calls, err)
	}
}

func TestAnthropicReturnsOnlyCanonicalTextAfterAdaptiveThinking(t *testing.T) {
	client, _ := NewAnthropic("claude-test", "synthetic-key", "")
	client.client.Transport = anthropicRoundTrip(func(*http.Request) (*http.Response, error) {
		return anthropicReply(200, `{"type":"message","role":"assistant","model":"claude-test","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"private summary","signature":"opaque"},{"type":"redacted_thinking","data":"opaque"},{"type":"text","text":"Thursday"}],"usage":{"input_tokens":22,"output_tokens":100}}`), nil
	})
	response, err := client.Generate(context.Background(), Request{System: "system", User: "user", MaxOutputTokens: 128})
	if err != nil || response.Text != "Thursday" || response.OutputTokens != 100 {
		t.Fatalf("adaptive thinking leaked into or blocked the answer: %+v %v", response, err)
	}
}

type anthropicCanceledBody struct{ cancel context.CancelFunc }

func (b anthropicCanceledBody) Read([]byte) (int, error) { b.cancel(); return 0, context.Canceled }
func (anthropicCanceledBody) Close() error               { return nil }

func TestAnthropicPreservesCancellationDuringResponseRead(t *testing.T) {
	client, _ := NewAnthropic("claude-test", "synthetic-key", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.client.Transport = anthropicRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: anthropicCanceledBody{cancel}}, nil
	})
	if _, err := client.Generate(ctx, Request{System: "system", User: "user", MaxOutputTokens: 128}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation became a provider response failure: %v", err)
	}
}

func TestAnthropicModelDiscoveryRejectsRepeatedCursorsAndMalformedCatalog(t *testing.T) {
	client, _ := NewAnthropic("discovery", "synthetic-key", "")
	for _, body := range []string{
		`{"data":[{"type":"model","id":"same","display_name":"First"},{"type":"model","id":"same","display_name":"Second"}],"has_more":false}`,
		`{"data":[],"has_more":true,"last_id":"stuck"}`,
		`{"data":[{"type":"model","id":"first","display_name":"First"}],"has_more":true,"last_id":"different"}`,
	} {
		calls := 0
		client.client.Transport = anthropicRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return anthropicReply(200, body), nil })
		if _, err := client.ListModels(context.Background()); !errors.Is(err, ErrAnthropicResponse) || calls != 1 {
			t.Fatalf("malformed catalog allowed repeated calls: calls=%d err=%v", calls, err)
		}
	}
}
