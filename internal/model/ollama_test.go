package model

import (
	"context"
	"encoding/json"
	"github.com/hazcod/blur/internal/model/modeltest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testChatRequest struct {
	Model    string `json:"model"`
	Think    *bool  `json:"think"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Format    string  `json:"format"`
	Stream    bool    `json:"stream"`
	KeepAlive *string `json:"keep_alive"`
	Options   struct {
		Temperature float32 `json:"temperature"`
		NumPredict  int     `json:"num_predict"`
		NumCtx      int     `json:"num_ctx"`
	} `json:"options"`
}

func TestOllamaGenerateUsesLoopbackJSONChatWithFixedBudgets(t *testing.T) {
	called := false
	server := httptest.NewServer(modeltest.LocalOllama("qwen3:4b", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
			t.Fatalf("request target = %s %s", r.Method, r.URL.Path)
		}
		var got testChatRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.Model != "qwen3:4b" || got.Think == nil || *got.Think || got.Stream || got.Format != "json" || len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[0].Content != "Answer only from evidence." || got.Messages[1].Role != "user" || got.Messages[1].Content != `{"question":"when?"}` {
			t.Fatalf("unexpected request: %+v", got)
		}
		if got.Options.Temperature != 0 || got.Options.NumPredict != MaxOutputTokens || got.Options.NumCtx != 1024 {
			t.Fatalf("request-sized or deterministic options = %+v, want a 1024-token minimum window", got.Options)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"{\"answer\":\"Tuesday\",\"source_ids\":[\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"]}"},"done":true,"prompt_eval_count":37,"eval_count":18}`))
	})))
	defer server.Close()

	generator, err := NewOllama(server.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	defer generator.CloseIdleConnections()
	got, err := generator.Generate(context.Background(), Request{System: "Answer only from evidence.", User: `{"question":"when?"}`, Format: "json", MaxOutputTokens: MaxOutputTokens})
	if err != nil {
		t.Fatal(err)
	}
	if !called || got.Text != `{"answer":"Tuesday","source_ids":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]}` || got.InputTokens != 37 || got.OutputTokens != 18 {
		t.Fatalf("unexpected generation: called=%t result=%+v", called, got)
	}
}

func TestOllamaExplicitKeepAliveIsBoundedAndBackgroundKeepsProviderDefault(t *testing.T) {
	for _, test := range []struct {
		name     string
		duration time.Duration
		wire     string
		invalid  bool
	}{
		{name: "background default"},
		{name: "interactive ten minutes", duration: 10 * time.Minute, wire: "10m0s"},
		{name: "negative", duration: -time.Second, invalid: true},
		{name: "unbounded residency rejected", duration: 24 * time.Hour, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests, chats := 0, 0
			local := modeltest.LocalOllama("fixture", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				chats++
				var wire testChatRequest
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
					t.Fatal(err)
				}
				if test.duration == 0 {
					if wire.KeepAlive != nil {
						t.Error("background request overrode provider residency default")
					}
				} else if wire.KeepAlive == nil || *wire.KeepAlive != test.wire {
					t.Errorf("explicit residency was not forwarded: keep_alive=%v want=%q", wire.KeepAlive, test.wire)
				}
				_, _ = w.Write([]byte(`{"done":true,"message":{"content":"ok"}}`))
			}))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; local.ServeHTTP(w, r) }))
			defer server.Close()
			generator, err := NewOllama(server.URL, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			defer generator.CloseIdleConnections()
			_, err = generator.Generate(context.Background(), Request{System: "policy", User: "synthetic", MaxOutputTokens: 8, KeepAlive: test.duration})
			if test.invalid {
				if err != ErrOllamaConfig || requests != 0 {
					t.Fatal("invalid residency contacted the provider", requests, err)
				}
			} else if err != nil || chats != 1 {
				t.Fatal("valid request failed", chats, err)
			}
		})
	}
}

func TestOllamaContextWindowScalesAndRoundsToRequestBudget(t *testing.T) {
	for _, test := range []struct {
		input, output, want int
	}{
		{50, 192, 1024},
		{1600, 192, 2304},
		{6000, 192, 6912},
		{ContextTokenLimit, MaxOutputTokens, ContextTokenLimit},
	} {
		if got := ollamaContextSize(test.input, test.output); got != test.want {
			t.Errorf("ollamaContextSize(%d, %d) = %d, want %d", test.input, test.output, got, test.want)
		}
	}
}

func TestNewOllamaRejectsNonLoopbackEndpoints(t *testing.T) {
	for _, endpoint := range []string{"https://localhost:11434", "http://192.0.2.1:11434", "http://user@127.0.0.1:11434", "http://127.0.0.1:11434?x=y", "http://127.0.0.1:11434/path"} {
		if _, err := NewOllama(endpoint, "model"); err == nil {
			t.Fatalf("unsafe endpoint accepted: %q", endpoint)
		}
	}
}

func TestNewOllamaAcceptsRootTrailingSlashAndNormalizesEndpoint(t *testing.T) {
	generator, err := NewOllama("http://127.0.0.1:11434/", "model")
	if err != nil {
		t.Fatalf("root trailing slash rejected: %v", err)
	}
	if generator.endpoint != "http://127.0.0.1:11434" {
		t.Fatalf("normalized endpoint = %q", generator.endpoint)
	}
}

func TestOllamaRejectsOverBudgetBeforeNetwork(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	generator, err := NewOllama(server.URL, "model")
	if err != nil {
		t.Fatal(err)
	}
	defer generator.CloseIdleConnections()
	_, err = generator.Generate(context.Background(), Request{System: "system", User: strings.Repeat("x", ContextTokenLimit), Format: "json", MaxOutputTokens: MaxOutputTokens})
	if err == nil || called {
		t.Fatalf("over-budget request error=%v network-called=%t", err, called)
	}
}

func TestOllamaResponseLimitAndStrictAnswerShape(t *testing.T) {
	for name, body := range map[string]string{
		"oversized": strings.Repeat("x", MaxResponseBytes+1),
		"malformed": `{"message":`,
		"trailing":  `{"message":{"content":"{}"},"done":true} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(modeltest.LocalOllama("model", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })))
			defer server.Close()
			generator, err := NewOllama(server.URL, "model")
			if err != nil {
				t.Fatal(err)
			}
			defer generator.CloseIdleConnections()
			if _, err := generator.Generate(context.Background(), Request{System: "s", User: "u", Format: "json", MaxOutputTokens: MaxOutputTokens}); err == nil {
				t.Fatal("invalid model response accepted")
			}
		})
	}
}
