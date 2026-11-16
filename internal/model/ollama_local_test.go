package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOllamaAnswersRefuseCloudBeforeQuestionOrEvidence(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		tagRemote, showRemote, notCompletion bool
		want                                 error
	}{
		{name: "cloud tag", tagRemote: true, want: ErrOllamaRemoteModel},
		{name: "cloud metadata", showRemote: true, want: ErrOllamaRemoteModel},
		{name: "wrong capability", notCompletion: true, want: ErrOllamaNotCompletion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var chats atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), "synthetic-capture-marker") {
					t.Error("evidence reached the model before local verification")
				}
				switch r.URL.Path {
				case "/api/tags":
					tag := map[string]string{"name": "answer:latest", "digest": strings.Repeat("a", 64)}
					if tc.tagRemote {
						tag["remote_host"] = "https://cloud.example"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{tag}})
				case "/api/show":
					capability := "completion"
					if tc.notCompletion {
						capability = "embedding"
					}
					details := map[string]any{"capabilities": []string{capability}}
					if tc.showRemote {
						details["remote_host"] = "https://cloud.example"
					}
					_ = json.NewEncoder(w).Encode(details)
				case "/api/chat":
					chats.Add(1)
				}
			}))
			defer server.Close()
			client, _ := NewOllama(server.URL, "answer")
			defer client.CloseIdleConnections()
			_, err := client.Generate(context.Background(), Request{System: "policy", User: "synthetic-capture-marker", MaxOutputTokens: 50})
			if !errors.Is(err, tc.want) || chats.Load() != 0 {
				t.Fatalf("cloud preflight err=%v chats=%d", err, chats.Load())
			}
		})
	}
}

func TestOllamaAnswerRejectsManifestReplacementAfterInference(t *testing.T) {
	var tags atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			digest := strings.Repeat("a", 64)
			if tags.Add(1) > 1 {
				digest = strings.Repeat("b", 64)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "answer:latest", "digest": digest}}})
		case "/api/show":
			_, _ = w.Write([]byte(`{"capabilities":["completion"]}`))
		case "/api/chat":
			_, _ = w.Write([]byte(`{"message":{"content":"synthetic answer"},"done":true}`))
		}
	}))
	defer server.Close()
	client, _ := NewOllama(server.URL, "answer")
	defer client.CloseIdleConnections()
	if _, err := client.Generate(context.Background(), Request{System: "policy", User: "question", MaxOutputTokens: 50}); !errors.Is(err, ErrOllamaModelChanged) {
		t.Fatalf("changed manifest accepted: %v", err)
	}
}
