package assistant

import (
	"context"
	"encoding/json"
	"github.com/hazcod/blur/internal/model/modeltest"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hazcod/blur/internal/model"
)

type appleBudgetProbe struct {
	estimate, exact int
	exactCalls      int
}

func (p *appleBudgetProbe) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}
func (p *appleBudgetProbe) CountInputTokens(context.Context, string, string) (int, error) {
	p.exactCalls++
	return p.exact, nil
}
func (p *appleBudgetProbe) CountRequestTokens(context.Context, model.Request) (int, error) {
	p.exactCalls++
	return p.exact, nil
}
func (*appleBudgetProbe) CloseIdleConnections()                             {}
func (*appleBudgetProbe) Identity(context.Context) (string, error)          { return "synthetic", nil }
func (*appleBudgetProbe) ContextTokenLimit(context.Context) (int, error)    { return 4096, nil }
func (p *appleBudgetProbe) EstimateRequestTokens(model.Request) (int, bool) { return p.estimate, true }

func TestAppleMemoryTokenCountUsesLocalEstimateOnlyWithBudgetMargin(t *testing.T) {
	const margin = 128
	probe := &appleBudgetProbe{estimate: 150, exact: 980}
	generator := &OllamaGenerator{client: probe, schema: json.RawMessage(appleAnswerSchema)}
	counter, ok := any(generator).(interface {
		CountMemoryInputTokensWithinBudget(context.Context, string, string, int) (int, error)
	})
	if !ok {
		t.Fatal("Apple answer counter has no budget-aware fast path")
	}
	const user = `{"evidence":[{"source_id":"S1"}]}`
	count, err := counter.CountMemoryInputTokensWithinBudget(context.Background(), "policy", user, 2048)
	if err != nil || count < probe.estimate || count > probe.estimate+margin || probe.exactCalls != 0 {
		t.Fatalf("short input count=%d exact calls=%d error=%v", count, probe.exactCalls, err)
	}
	count, err = counter.CountMemoryInputTokensWithinBudget(context.Background(), "policy", user, probe.estimate+margin-1)
	if err != nil || count != probe.exact || probe.exactCalls != 1 {
		t.Fatalf("tight-budget count=%d exact calls=%d error=%v; want exact tokenizer", count, probe.exactCalls, err)
	}
}

func TestOllamaGeneratorDecodesStructuredCitedAnswer(t *testing.T) {
	server := httptest.NewServer(modeltest.LocalOllama("qwen3:4b", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"content":"{\"answer\":\"Tuesday\",\"source_ids\":[\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"]}"},"done":true,"prompt_eval_count":38,"eval_count":12}`))
	})))
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
	server := httptest.NewServer(modeltest.LocalOllama("qwen3:4b", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"content":"{\"answer\":\"Thursday\",\"source_ids\":[\"S1\"]}"},"done":true,"prompt_eval_count":38,"eval_count":12}`))
	})))
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

func TestOllamaGeneratorResolvesSelectedCitationsFromAdmittedEvidence(t *testing.T) {
	server := httptest.NewServer(modeltest.LocalOllama("qwen3:4b", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"content":"{\"answer\":\"The CLI repository builds and tests the command-line app.\",\"source_labels\":[\"S2\"],\"insufficient_evidence\":false}"},"done":true,"prompt_eval_count":38,"eval_count":12}`))
	})))
	defer server.Close()
	generator, err := NewOllamaGenerator(server.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	defer generator.CloseIdleConnections()
	first, second := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	packed := PackedContext{System: "Use evidence.", User: `{"question":"what is the CLI repo about?","evidence":[{"text":"CLI application"},{"text":"Tests"}]}`, SourceIDs: []string{first, second}}
	packed.InputBytes = len(packed.System) + len(packed.User)
	got, err := generator.Generate(context.Background(), packed, MaxAnswerTokens)
	if err != nil || got.Text != "The CLI repository builds and tests the command-line app." || len(got.SourceIDs) != 1 || got.SourceIDs[0] != second {
		t.Fatalf("model-selected citations=%+v error=%v", got, err)
	}
}

func TestOllamaGeneratorResolvesOnlyModelSelectedEvidenceLabels(t *testing.T) {
	server := httptest.NewServer(modeltest.LocalOllama("model", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"content":"{\"answer\":\"The security channel discussed a browser vulnerability.\",\"source_labels\":[\"S2\"],\"insufficient_evidence\":false}"},"done":true}`))
	})))
	defer server.Close()
	generator, err := NewOllamaGenerator(server.URL, "model")
	if err != nil {
		t.Fatal(err)
	}
	defer generator.CloseIdleConnections()
	first, second := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	packed := PackedContext{System: "Use labelled evidence.", User: `{"evidence":[{"label":"S1"},{"label":"S2"}]}`, SourceIDs: []string{first, second}}
	packed.InputBytes = len(packed.System) + len(packed.User)
	got, err := generator.Generate(context.Background(), packed, MaxAnswerTokens)
	if err != nil || len(got.SourceIDs) != 1 || got.SourceIDs[0] != second {
		t.Fatalf("selected citations=%v error=%v; want only second source", got.SourceIDs, err)
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

func TestOllamaGeneratorRejectsAnswerWithoutEvidenceLabels(t *testing.T) {
	server := httptest.NewServer(modeltest.LocalOllama("model", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"content":"{\"answer\":\"Tuesday\",\"source_ids\":[]}"},"done":true}`))
	})))
	defer server.Close()
	generator, err := NewOllamaGenerator(server.URL, "model")
	if err != nil {
		t.Fatal(err)
	}
	defer generator.CloseIdleConnections()
	packed := PackedContext{System: "s", User: "u", SourceIDs: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	packed.InputBytes = len(packed.System) + len(packed.User)
	got, err := generator.Generate(context.Background(), packed, MaxAnswerTokens)
	if err == nil || len(got.SourceIDs) != 0 {
		t.Fatalf("uncited answer was accepted as grounded: %+v %v", got, err)
	}
}
