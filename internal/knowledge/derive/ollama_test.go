package derive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/knowledge"
)

func TestOllamaGeneratorSendsOnlyBoundedEvidenceAndValidatesCitations(t *testing.T) {
	source := sourceID('a')
	input := []Evidence{{SourceID: source, Text: "Project Atlas is owned by Mina."}}
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
			t.Fatalf("request target = %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Options struct {
				NumPredict int `json:"num_predict"`
			} `json:"options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" || !strings.Contains(body.Messages[1].Content, "Project Atlas is owned by Mina.") || body.Options.NumPredict != MaxOutputTokens {
			t.Fatalf("unexpected bounded request: %+v", body)
		}
		content := fmt.Sprintf(`{"suggestions":[{"kind":"project","name":"Project Atlas","fact":"Mina owns the project.","source_id":%q}]}`, source)
		fmt.Fprintf(w, `{"message":{"content":%q},"done":true,"prompt_eval_count":100,"eval_count":30}`, content)
	}))
	defer server.Close()
	generator, err := NewOllamaGenerator(server.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	defer generator.CloseIdleConnections()
	if tokens, err := generator.CountInputTokens(context.Background(), input); err != nil || tokens <= 0 {
		t.Fatalf("CountInputTokens() = %d, %v", tokens, err)
	}
	got, err := generator.Generate(context.Background(), input)
	if err != nil || len(got) != 1 || got[0].Kind != knowledge.Project || got[0].SourceID != source || requests != 1 {
		t.Fatalf("Generate() = %+v, %v, requests=%d", got, err, requests)
	}
}

func TestOllamaGeneratorRejectsUnknownModelFieldsAndUncitedSources(t *testing.T) {
	source := sourceID('a')
	for name, response := range map[string]string{
		"unknown state field": `{"suggestions":[{"kind":"project","name":"Atlas","fact":"A fact.","source_id":"` + source + `","state":"confirmed"}]}`,
		"uncited source":      `{"suggestions":[{"kind":"project","name":"Atlas","fact":"A fact.","source_id":"` + sourceID('b') + `"}]}`,
		"trailing JSON":       `{"suggestions":[]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{"message":{"content":%q},"done":true,"prompt_eval_count":20,"eval_count":10}`, response)
			}))
			defer server.Close()
			generator, err := NewOllamaGenerator(server.URL, "qwen3:4b")
			if err != nil {
				t.Fatal(err)
			}
			defer generator.CloseIdleConnections()
			if _, err := generator.Generate(context.Background(), []Evidence{{SourceID: source, Text: "Project Atlas."}}); err == nil {
				t.Fatal("invalid suggestion response accepted")
			}
		})
	}
}
