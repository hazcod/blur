package assistant

import (
	"context"
	"encoding/json"
	"github.com/hazcod/blur/internal/model/modeltest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type generalModeAppLister struct {
	testRetriever
	listCalls int
}

func (r *generalModeAppLister) KnownApps(context.Context) ([]KnownApp, error) {
	r.listCalls++
	return []KnownApp{{ID: "com.example.app"}}, nil
}

func TestExplicitGeneralModeSkipsPersonalAppDiscovery(t *testing.T) {
	retriever := &generalModeAppLister{}
	generator := &testGeneralGenerator{answer: "A general answer."}
	result, err := NewService(retriever, generator, testCounter{tokens: 40}, 8192).AskWithHistoryMode(context.Background(), "Explain DNS", nil, "general")
	if err != nil || !result.General || retriever.listCalls != 0 || retriever.calls != 0 || generator.calls != 1 {
		t.Fatalf("general Ask consulted personal memory before answering: result=%+v app_lists=%d retrievals=%d generations=%d err=%v", result, retriever.listCalls, retriever.calls, generator.calls, err)
	}
}

func TestDetailedGeneralAnswerHasEnoughOutputBudget(t *testing.T) {
	server := httptest.NewServer(modeltest.LocalOllama("test-model", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Options struct {
				NumPredict int `json:"num_predict"`
			} `json:"options"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Options.NumPredict < 768 {
			http.Error(w, "insufficient output budget", http.StatusBadRequest)
			return
		}
		answer := strings.Repeat("A complete technical explanation needs several steps. ", 40)
		_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "message": map[string]string{"content": answer}, "eval_count": 500})
	})))
	defer server.Close()
	g, err := NewOllamaGenerator(server.URL, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	defer g.CloseIdleConnections()
	result, err := NewService(&testRetriever{}, g, g, 8192).Ask(context.Background(), "Explain database isolation levels in detail.")
	if err != nil || !result.General || len(result.Answer.Text) < 1500 {
		t.Fatal(result, err)
	}
}

func TestGeneralFollowupUsesOnlyAdmittedConsecutiveGeneralConversation(t *testing.T) {
	requests := make(chan string, 2)
	server := httptest.NewServer(modeltest.LocalOllama("fixture", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&wire) != nil || len(wire.Messages) != 2 {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests <- wire.Messages[1].Content
		_, _ = w.Write([]byte(`{"done":true,"message":{"content":"func Answer() int { return 43 }"}}`))
	})))
	defer server.Close()
	g, err := NewOllamaGenerator(server.URL, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer g.CloseIdleConnections()
	history := []Turn{
		{Question: "Earlier general question", Answer: "OLD_GENERAL_BEFORE_PRIVATE", General: true, Plan: Plan{Kind: KindGeneral}},
		{Question: "What did I read?", Answer: "PRIVATE_CAPTURE_SENTINEL", Plan: Plan{Kind: KindLookup}},
		{Question: "Write a Go function returning 42", Answer: "func Answer() int { return 42 }", General: true, Plan: Plan{Kind: KindGeneral}},
	}
	for _, admitted := range []bool{true, false} {
		result, err := NewService(&testRetriever{}, g, g, 8192).WithPersonalContext(admitted).AskWithHistoryMode(context.Background(), "Make it return 43 instead.", history, "general")
		if err != nil {
			t.Fatal(err)
		}
		wire := <-requests
		if strings.Contains(wire, "PRIVATE_CAPTURE_SENTINEL") || strings.Contains(wire, "OLD_GENERAL_BEFORE_PRIVATE") {
			t.Fatal("general generation crossed a private-memory boundary", wire)
		}
		if admitted {
			var payload struct {
				Question     string                              `json:"question"`
				Conversation []struct{ Question, Answer string } `json:"conversation"`
			}
			if err := json.Unmarshal([]byte(wire), &payload); err != nil || payload.Question != "Make it return 43 instead." || len(payload.Conversation) != 1 || payload.Conversation[0].Answer != "func Answer() int { return 42 }" || result.ConversationTurns != 1 || result.ConversationOmitted != 0 {
				t.Fatal("follow-up lost its previous general answer", wire, result, err)
			}
		} else if wire != "Make it return 43 instead." || result.ConversationTurns != 0 {
			t.Fatal("conversation context was added without admission", wire, result)
		}
	}
}

func TestLongGeneralFollowupFitsBudgetAndDisclosesExcerptedHistory(t *testing.T) {
	requests := make(chan string, 1)
	server := httptest.NewServer(modeltest.LocalOllama("fixture", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&wire) != nil || len(wire.Messages) != 2 {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests <- wire.Messages[1].Content
		_, _ = w.Write([]byte(`{"done":true,"message":{"content":"A shorter explanation."}}`))
	})))
	defer server.Close()
	g, err := NewOllamaGenerator(server.URL, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer g.CloseIdleConnections()
	question := "Rewrite it with these requirements: " + strings.Repeat("explicit cancellation; ", 120)
	prior := strings.Repeat("A detailed general explanation with code. ", 175)
	result, err := NewService(&testRetriever{}, g, g, 8192).WithPersonalContext(true).AskWithHistoryMode(context.Background(), question, []Turn{{Question: "Explain Go contexts.", Answer: prior, General: true, Plan: Plan{Kind: KindGeneral}}}, "general")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Question     string `json:"question"`
		Conversation []struct {
			Answer    string
			Truncated bool
		} `json:"conversation"`
	}
	if err := json.Unmarshal([]byte(<-requests), &payload); err != nil || payload.Question != strings.TrimSpace(question) || len(payload.Conversation) != 1 || !payload.Conversation[0].Truncated || result.ConversationTruncated != 1 || result.InputTokens+MaxAnswerTokens+PromptTemplateReserve > 8192 {
		t.Fatal("bounded follow-up lost input or hid omitted context", result, err)
	}
}

func TestOllamaGeneralAnswerSendsOnlyQuestionWithoutStructuredContext(t *testing.T) {
	type request struct {
		Format   string `json:"format"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	called := false
	server := httptest.NewServer(modeltest.LocalOllama("test-model", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		var got request
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.Format != "" || len(got.Messages) != 2 || got.Messages[0].Content != generalSystemPrompt || got.Messages[1].Role != "user" || got.Messages[1].Content != "What is 2+2?" {
			t.Fatalf("general request contained unexpected context: %+v", got)
		}
		_, _ = w.Write([]byte(`{"message":{"content":"2 + 2 = 4."},"done":true,"prompt_eval_count":80,"eval_count":7}`))
	})))
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
