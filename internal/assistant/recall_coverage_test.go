package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/model/modeltest"
)

func TestMemoryLookupDisclosesExcerptedOrOmittedEvidence(t *testing.T) {
	for _, generated := range []bool{false, true} {
		for _, kind := range []string{"excerpted", "omitted", "complete"} {
			t.Run(fmt.Sprintf("%s/generated=%t", kind, generated), func(t *testing.T) {
				id := strings.Repeat("a", 64)
				evidence := []Evidence{{SourceID: id, Text: "Mira owns rollback."}}
				switch kind {
				case "excerpted":
					evidence[0].Text += strings.Repeat(" Source context.", 700)
				case "omitted":
					for i := 1; i <= MaxEvidenceItems; i++ {
						evidence = append(evidence, Evidence{SourceID: fmt.Sprintf("%064x", i), Text: "A related saved observation."})
					}
				}
				r := taskTargetRetriever{Retrieval{Evidence: evidence}}
				var g Generator
				if generated {
					g = &testGenerator{answer: ModelAnswer{Text: "Mira owns rollback.", SourceIDs: []string{id}}}
				}
				result, err := NewService(r, g, testCounter{tokens: 1}, 8192).AskWithHistoryMode(context.Background(), "What do you remember about rollback?", nil, "memory")
				if err != nil {
					t.Fatal(err)
				}
				if result.Plan.Kind == KindRecap || result.General || len(result.Sources) == 0 || result.Sources[0].Text != evidence[0].Text {
					t.Fatal("lookup changed route or lost full source inspection", result)
				}
				limited := kind != "complete"
				if result.CoverageLimited != limited || (limited && result.Omitted+result.Truncated == 0) {
					t.Fatalf("coverage=%t omitted=%d truncated=%d, want limited=%t", result.CoverageLimited, result.Omitted, result.Truncated, limited)
				}
			})
		}
	}
}

func TestBoundedMeaningRecallCoverageSurvivesQuotedAndGeneratedAnswers(t *testing.T) {
	id := strings.Repeat("a", 64)
	r := taskTargetRetriever{Retrieval{Evidence: []Evidence{{SourceID: id, Text: "Collect the coat from the drycleaners"}}, Diagnosis: Diagnosis{SemanticIssue: "limited"}}}
	for _, generated := range []bool{false, true} {
		var g Generator
		if generated {
			g = &testGenerator{answer: ModelAnswer{Text: "Collect the coat.", SourceIDs: []string{id}}}
		}
		result, err := NewService(r, g, testCounter{tokens: 1}, 8192).Ask(context.Background(), "What do you remember about laundry?")
		if err != nil || !result.CoverageLimited {
			t.Fatal("bounded recall was presented as exhaustive", generated, result, err)
		}
	}
}

func TestEmptyBoundedMeaningRecallDoesNotClaimAbsentMemory(t *testing.T) {
	text := EmptyAnswerText(Plan{Kind: KindLookup}, Diagnosis{SemanticIssue: "limited"})
	if !strings.Contains(text, "bounded") || !strings.Contains(text, "may") {
		t.Fatal("limited recall claimed absent memory", text)
	}
	r := taskTargetRetriever{Retrieval{Diagnosis: Diagnosis{SemanticIssue: "limited"}}}
	result, err := NewService(r, nil, nil, 8192).Ask(context.Background(), "What do you remember about laundry?")
	if err != nil || !result.CoverageLimited {
		t.Fatal("empty bounded recall lost coverage flag", result, err)
	}
}

func TestOllamaRecallCanConstrainJSONWithoutChangingSelectedModel(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"queries":{"type":"array","items":{"type":"string"}}},"required":["queries"],"additionalProperties":false}`)
	server := httptest.NewServer(modeltest.LocalOllama("test-model", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model  string          `json:"model"`
			Format json.RawMessage `json:"format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "test-model" || string(request.Format) != string(schema) {
			t.Error("recall changed model or omitted schema", request)
		}
		_, _ = w.Write([]byte(`{"message":{"content":"{\"queries\":[\"dry cleaning\"]}"},"done":true,"prompt_eval_count":20,"eval_count":8}`))
	})))
	defer server.Close()
	g, err := NewOllamaGenerator(server.URL, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	defer g.CloseIdleConnections()
	structured, ok := any(g).(interface {
		GenerateGeneralJSONPrompt(context.Context, string, string, json.RawMessage, int) (string, error)
	})
	if !ok {
		t.Fatal("selected model cannot request constrained recall JSON")
	}
	output, err := structured.GenerateGeneralJSONPrompt(context.Background(), "Return recall JSON", "laundry", schema, 512)
	if err != nil || output != `{"queries":["dry cleaning"]}` {
		t.Fatal(output, err)
	}
}
