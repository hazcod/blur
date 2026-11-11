package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaGeneratorDecodesStructuredCitedAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"content":"{\"answer\":\"Tuesday\",\"source_ids\":[\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"]}"},"done":true,"prompt_eval_count":38,"eval_count":12}`))
	}))
	defer server.Close()
	generator, err := NewOllamaGenerator(server.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	defer generator.CloseIdleConnections()
	packed := PackedContext{
		System:    "Use evidence.",
		User:      `{"question":"when?","evidence":[]}`,
		SourceIDs: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	packed.InputBytes = len(packed.System) + len(packed.User)
	got, err := generator.Generate(context.Background(), packed, MaxAnswerTokens)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "Tuesday" || len(got.SourceIDs) != 1 || got.SourceIDs[0] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("decoded model answer=%+v", got)
	}
}

func TestOllamaGeneratorResolvesCompactCitationLabelsToCanonicalIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"content":"{\"answer\":\"Thursday\",\"source_ids\":[\"S1\"]}"},"done":true,"prompt_eval_count":38,"eval_count":12}`))
	}))
	defer server.Close()
	generator, err := NewOllamaGenerator(server.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	defer generator.CloseIdleConnections()
	packed := PackedContext{
		System:    "Use evidence labels S1 and S2.",
		User:      `{"question":"when?","evidence":[{"source_id":"S1"}]}`,
		SourceIDs: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	packed.InputBytes = len(packed.System) + len(packed.User)
	got, err := generator.Generate(context.Background(), packed, MaxAnswerTokens)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SourceIDs) != 1 || got.SourceIDs[0] != packed.SourceIDs[0] {
		t.Fatalf("resolved citations = %v, want canonical content ID", got.SourceIDs)
	}
}

func TestOllamaGeneratorRejectsMalformedOrOverBudgetPackedContext(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"message":{"content":"{\"answer\":\"Tuesday\",\"source_ids\":[]}"},"done":true}`))
	}))
	defer server.Close()
	generator, err := NewOllamaGenerator(server.URL, "model")
	if err != nil {
		t.Fatal(err)
	}
	defer generator.CloseIdleConnections()
	packed := PackedContext{System: "s", User: "u", SourceIDs: []string{"bad"}}
	packed.InputBytes = len(packed.System) + len(packed.User)
	_, err = generator.Generate(context.Background(), packed, MaxAnswerTokens)
	if err == nil || called {
		t.Fatalf("malformed packed context err=%v network-called=%t", err, called)
	}
}

func TestOllamaGeneratorRejectsInvalidGeneratedAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"content":"{\"answer\":\"Tuesday\",\"source_ids\":[]}"},"done":true}`))
	}))
	defer server.Close()
	generator, err := NewOllamaGenerator(server.URL, "model")
	if err != nil {
		t.Fatal(err)
	}
	defer generator.CloseIdleConnections()
	packed := PackedContext{System: "s", User: "u", SourceIDs: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	packed.InputBytes = len(packed.System) + len(packed.User)
	_, err = generator.Generate(context.Background(), packed, MaxAnswerTokens)
	if err == nil {
		t.Fatal("answer without a citation was accepted")
	}
}
