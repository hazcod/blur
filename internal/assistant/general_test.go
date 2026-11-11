package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaGeneralAnswerSendsOnlyQuestionWithoutStructuredContext(t *testing.T) {
	type request struct {
		Format   string `json:"format"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		var got request
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.Format != "" || len(got.Messages) != 2 || got.Messages[0].Content != generalSystemPrompt || got.Messages[1].Role != "user" || got.Messages[1].Content != "What is 2+2?" {
			t.Fatalf("general request contained unexpected context: %+v", got)
		}
		_, _ = w.Write([]byte(`{"message":{"content":"2 + 2 = 4."},"done":true,"prompt_eval_count":80,"eval_count":7}`))
	}))
	defer server.Close()
	generator, err := NewOllamaGenerator(server.URL, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	defer generator.CloseIdleConnections()
	answer, err := generator.GenerateGeneral(context.Background(), "What is 2+2?", generalAnswerTokens)
	if err != nil || !called || answer != "2 + 2 = 4." {
		t.Fatalf("GenerateGeneral()=%q called=%t err=%v", answer, called, err)
	}
}

func TestOpenAICompatibleGeneralAnswerSendsOnlyQuestion(t *testing.T) {
	type request struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		var got request
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if len(got.Messages) != 2 || got.Messages[0].Content != generalSystemPrompt || got.Messages[1].Role != "user" || got.Messages[1].Content != "What is 2+2?" {
			t.Fatalf("general request contained unexpected context: %+v", got)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"2 + 2 = 4."}}]}`))
	}))
	defer server.Close()
	generator, err := NewOpenAICompatibleGenerator(server.URL, "test-model", "")
	if err != nil {
		t.Fatal(err)
	}
	defer generator.CloseIdleConnections()
	answer, err := generator.GenerateGeneral(context.Background(), "What is 2+2?", generalAnswerTokens)
	if err != nil || !called || answer != "2 + 2 = 4." {
		t.Fatalf("GenerateGeneral()=%q called=%t err=%v", answer, called, err)
	}
}
