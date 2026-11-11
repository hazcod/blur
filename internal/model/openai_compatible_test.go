package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompatibleInputPreflightLimitIsBoundedAndOverflowSafe(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name       string
		system     int
		user       int
		wantWithin bool
	}{
		{name: "empty", wantWithin: true},
		{name: "exact cap", system: maxCompatibleRequestBytes - 8, user: 8, wantWithin: true},
		{name: "over cap", system: maxCompatibleRequestBytes - 8, user: 9},
		{name: "negative size", system: -1},
		{name: "integer overflow", system: maxInt, user: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := compatibleInputWithinPreflightLimit(tc.system, tc.user); got != tc.wantWithin {
				t.Fatalf("compatibleInputWithinPreflightLimit(%d,%d)=%t, want %t", tc.system, tc.user, got, tc.wantWithin)
			}
		})
	}
}

func TestOpenAICompatibleSendsBoundedChatRequestAndParsesSingleChoice(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer local-test-key" {
			t.Fatalf("request target or credential header is incorrect: %s %s", r.Method, r.URL.Path)
		}
		var request compatibleRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "test-model" || request.MaxTokens != 128 || request.Stream || len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[0].Content != "Use only evidence." || request.Messages[1].Role != "user" || request.Messages[1].Content != `{"question":"when?"}` {
			t.Fatalf("unexpected compatible request: %+v", request)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"answer\":\"Tuesday\"}"}}]}`))
	}))
	defer server.Close()

	client, err := NewOpenAICompatible(server.URL+"/v1", "test-model", "local-test-key")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	got, err := client.Generate(context.Background(), "Use only evidence.", `{"question":"when?"}`, 128)
	if err != nil || !called || got != `{"answer":"Tuesday"}` {
		t.Fatalf("Generate()=%q called=%t err=%v", got, called, err)
	}
}

func TestOpenAICompatibleRequiresSecureRemoteEndpointAndBoundsInputBeforeRequest(t *testing.T) {
	for _, endpoint := range []string{"http://example.test/v1", "https://example.test/v1"} {
		if _, err := NewOpenAICompatible(endpoint, "model", ""); err == nil {
			t.Fatalf("insecure or credentialless remote endpoint accepted: %q", endpoint)
		}
	}

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client, err := NewOpenAICompatible(server.URL, "model", "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	large := strings.Repeat("x", maxCompatibleRequestBytes+1)
	if _, err := client.CountInputTokens(context.Background(), "system", large); err == nil {
		t.Fatal("oversized prompt passed token counting")
	}
	if _, err := client.Generate(context.Background(), "system", large, 128); err == nil || called {
		t.Fatalf("oversized request err=%v network-called=%t", err, called)
	}
}
