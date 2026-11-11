package notes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/capture"
)

type testIPResolver []net.IPAddr

func (r testIPResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) { return r, nil }

func TestLocalModelDialPinsOnlyResolvedLoopbackAddresses(t *testing.T) {
	var dialed []string
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, nil
	}
	resolver := testIPResolver{{IP: net.ParseIP("::1")}, {IP: net.ParseIP("127.0.0.1")}}
	if _, err := dialLoopback(context.Background(), "tcp", "localhost:11434", resolver, dial); err != nil {
		t.Fatalf("loopback dial: %v", err)
	}
	if len(dialed) != 1 || dialed[0] != "[::1]:11434" {
		t.Fatalf("dialed addresses = %v, want a vetted literal loopback address", dialed)
	}
}

func TestLocalModelDialRejectsAnyNonLoopbackDNSAnswerBeforeDial(t *testing.T) {
	var dialCalls int
	dial := func(context.Context, string, string) (net.Conn, error) { dialCalls++; return nil, nil }
	answers := testIPResolver{{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("203.0.113.10")}}
	_, err := dialLoopback(context.Background(), "tcp", "localhost:11434", answers, dial)
	if err == nil || !strings.Contains(err.Error(), "loopback") || dialCalls != 0 {
		t.Fatalf("mixed DNS answers: err=%v dial calls=%d", err, dialCalls)
	}
}

func TestLocalModelDialRejectsUnapprovedHostnames(t *testing.T) {
	var dialCalls int
	resolver := testIPResolver{{IP: net.ParseIP("127.0.0.1")}}
	dial := func(context.Context, string, string) (net.Conn, error) { dialCalls++; return nil, nil }
	_, err := dialLoopback(context.Background(), "tcp", "ollama.internal:11434", resolver, dial)
	if !errors.Is(err, ErrOllamaConfig) || dialCalls != 0 {
		t.Fatalf("unapproved hostname: err=%v dial calls=%d", err, dialCalls)
	}
}

func TestLocalModelTransportSendsNoBytesWhenLocalhostResolvesRemotely(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = io.WriteString(w, `{"response":"{\"proposals\":[]}"}`)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := capture.SanitizeObservation("sh.blur.notes", "local", "meeting notes", MaxNoteBytes)
	if err != nil {
		t.Fatal(err)
	}
	extractor := &OllamaExtractor{
		endpoint: "http://localhost:" + parsed.Port(), model: "test",
		client: &http.Client{Transport: newOllamaTransport(testIPResolver{{IP: net.ParseIP("203.0.113.10")}})},
	}
	if _, err := extractor.Extract(context.Background(), observation); !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("Extract() error = %v, want local model unavailable", err)
	}
	if requests != 0 {
		t.Fatalf("server received %d request(s) despite non-loopback resolution", requests)
	}
}

func TestOllamaExtractorUsesBoundedLocalOnlyRequest(t *testing.T) {
	var got ollamaRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/generate" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, `{"response":"{\"proposals\":[{\"kind\":\"reminder\",\"text\":\"Ask Sam\",\"due\":\"tomorrow\"},{\"kind\":\"question\",\"text\":\"Who owns the next step?\"}]}"}`)
	}))
	defer server.Close()

	extractor, err := NewOllamaExtractor(server.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	observation, err := capture.SanitizeObservation("sh.blur.notes", "local", "Ask Sam tomorrow", MaxNoteBytes)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := extractor.Extract(context.Background(), observation)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "qwen3:4b" || got.Stream || got.Options.NumPredict != maxOllamaOutputTokens || got.Options.NumCtx != maxOllamaContextTokens || len(got.Prompt)+ollamaTemplateOverhead+got.Options.NumPredict > got.Options.NumCtx || !strings.Contains(got.Prompt, observation.Text()) {
		t.Fatalf("unbounded or incomplete local request: %+v", got)
	}
	if len(proposals) != 2 || proposals[0].Kind != Reminder || proposals[0].Text != "Ask Sam" || proposals[1].Kind != Question {
		t.Fatalf("unexpected proposals: %+v", proposals)
	}
}

func TestOllamaExtractorRejectsOverBudgetPromptBeforeNetwork(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = io.WriteString(w, `{"response":"{\"proposals\":[]}"}`)
	}))
	defer server.Close()
	extractor, err := NewOllamaExtractor(server.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	observation, err := capture.SanitizeObservation("sh.blur.notes", "local", strings.Repeat("a", MaxNoteBytes), MaxNoteBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extractor.Extract(context.Background(), observation); err == nil || requests != 0 {
		t.Fatalf("over-budget note was sent: err=%v requests=%d", err, requests)
	}
}

func TestOllamaExtractorRejectsNonLoopbackAndMalformedOutput(t *testing.T) {
	for _, endpoint := range []string{"https://example.com", "http://example.com:11434", "http://localhost.evil:11434", "file:///tmp/model"} {
		if _, err := NewOllamaExtractor(endpoint, "model"); err == nil {
			t.Errorf("accepted non-local endpoint %q", endpoint)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"response":"{\"proposals\":[{\"kind\":\"shell\",\"text\":\"run command\"}]}"}`)
	}))
	defer server.Close()
	extractor, err := NewOllamaExtractor(server.URL, "model")
	if err != nil {
		t.Fatal(err)
	}
	observation, _ := capture.SanitizeObservation("sh.blur.notes", "local", "do a thing", MaxNoteBytes)
	if _, err := extractor.Extract(context.Background(), observation); err == nil {
		t.Fatal("invalid model output accepted")
	}
}
