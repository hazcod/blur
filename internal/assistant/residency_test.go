package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/model/modeltest"
)

func TestInteractiveLocalAnswerRequestsKeepModelWarm(t *testing.T) {
	for _, kind := range []string{"memory", "general", "rules", "prepared"} {
		t.Run(kind, func(t *testing.T) {
			chats := 0
			server := httptest.NewServer(modeltest.LocalOllama("fixture", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				chats++
				var wire struct {
					KeepAlive string `json:"keep_alive"`
				}
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil || wire.KeepAlive != "10m0s" {
					t.Errorf("interactive answer did not request bounded model residency: keep_alive=%q err=%v", wire.KeepAlive, err)
				}
				content := "A general answer."
				if kind == "memory" {
					content = `{"answer":"Mira owns rollback.","source_ids":["S1"],"insufficient_evidence":false}`
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "message": map[string]string{"content": content}})
			})))
			defer server.Close()
			g, err := NewOllamaGenerator(server.URL, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			defer g.CloseIdleConnections()
			switch kind {
			case "memory":
				packed, packErr := PackContext("Who is Mira?", []Evidence{{SourceID: strings.Repeat("a", 64), Text: "Mira owns rollback."}})
				if packErr != nil {
					t.Fatal(packErr)
				}
				_, err = g.Generate(context.Background(), packed, MaxAnswerTokens)
			case "general":
				_, err = g.GenerateGeneral(context.Background(), "What is SQLite?", generalAnswerTokens)
			case "rules":
				_, err = g.GenerateGeneralWithRules(context.Background(), "What is SQLite?", []string{"Use concise wording."}, generalAnswerTokens)
			case "prepared":
				_, err = g.GenerateGeneralPrompt(context.Background(), generalSystemPrompt, "What is SQLite?", generalAnswerTokens)
			}
			if err != nil || chats != 1 {
				t.Fatal(chats, err)
			}
		})
	}
}
