package notes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/capture"
)

func TestOpenAICompatibleExtractorUsesBoundedSanitizedNoteRequest(t *testing.T) {
	var got struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected provider request: %s %s authorization=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"proposals\":[{\"kind\":\"task\",\"text\":\"Send the plan\"}]}"}}]}`)
	}))
	defer server.Close()
	extractor, err := NewOpenAICompatibleExtractor(server.URL+"/v1", "model-x", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	observation, err := capture.SanitizeObservation("sh.blur.notes", "local", "TODO: Send the plan\npassword: ultrasecret", MaxNoteBytes)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := extractor.Extract(context.Background(), observation)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "model-x" || got.MaxTokens != maxOllamaOutputTokens || len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[1].Role != "user" {
		t.Fatalf("model request was not bounded or role-separated: %+v", got)
	}
	if strings.Contains(got.Messages[1].Content, "ultrasecret") || !strings.Contains(got.Messages[1].Content, "[REDACTED:SECRET]") {
		t.Fatal("provider received unsanitized note contents")
	}
	if len(proposals) != 1 || proposals[0].Text != "Send the plan" {
		t.Fatalf("unexpected proposals: %+v", proposals)
	}
}

func TestOpenAICompatibleExtractorRequiresHTTPSForRemoteEndpoint(t *testing.T) {
	for _, endpoint := range []string{"http://example.com/v1", "http://localhost.evil/v1", "https://user:pass@example.com/v1", "https://example.com/v1?secret=x"} {
		if _, err := NewOpenAICompatibleExtractor(endpoint, "model", "token"); err == nil {
			t.Errorf("accepted unsafe endpoint %q", endpoint)
		}
	}
	if _, err := NewOpenAICompatibleExtractor("https://api.example.com/v1", "model", ""); err == nil {
		t.Fatal("accepted a remote endpoint without a credential")
	}
}

func TestOpenAICompatibleExtractorNeverForwardsCredentialOnRedirect(t *testing.T) {
	var redirectedRequests int
	redirected := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { redirectedRequests++ }))
	defer redirected.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, nil, redirected.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	extractor, err := NewOpenAICompatibleExtractor(server.URL, "model", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	observation, _ := capture.SanitizeObservation("sh.blur.notes", "local", "a note", MaxNoteBytes)
	if _, err := extractor.Extract(context.Background(), observation); err == nil {
		t.Fatal("redirect response accepted")
	}
	if redirectedRequests != 0 {
		t.Fatalf("credential-bearing request followed redirect %d time(s)", redirectedRequests)
	}
}
