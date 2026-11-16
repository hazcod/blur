package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/model/modeltest"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// Omitting either surface's history or routing the popup continuation through
// note interpretation breaks this real encrypted-store/provider-boundary test.
func TestGroundedContinuationAcrossAskSurfaces(t *testing.T) {
	for _, surface := range []string{"dashboard", "popup", "popup to dashboard"} {
		t.Run(surface, func(t *testing.T) {
			var mu sync.Mutex
			var payloads []string
			server := httptest.NewServer(modeltest.LocalOllama("fixture", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode provider request: %v", err)
				}
				ranking := len(request.Messages) > 0 && strings.Contains(request.Messages[0].Content, "Rank saved evidence")
				combinedRecall := ranking && strings.Contains(request.Messages[0].Content, "alternate search")
				for _, message := range request.Messages {
					if message.Role == "user" {
						mu.Lock()
						if !ranking {
							payloads = append(payloads, message.Content)
						}
						mu.Unlock()
					}
				}
				response := `{"answer":"Mira owns rollback.","source_ids":["S1"],"insufficient_evidence":false}`
				if combinedRecall {
					response = `{"queries":["rollback owner","Mira"],"matches":[{"ref":"S1","score":0.98}]}`
				} else if ranking {
					response = `{"matches":[{"ref":"S1","score":0.98}]}`
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "message": map[string]string{"content": response}})
			})))
			defer server.Close()
			dir := t.TempDir()
			store, err := durable.OpenDurable(filepath.Join(dir, "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func(close func() error) { _ = close() }(store.Close)
			noteService := notes.NewService(store, nil, nil)
			controller := newLocalAIController(filepath.Join(dir, "ai.json"), noteService)
			defer controller.Close()
			c := NewClient(store).WithNotes(noteService).WithLocalAI(controller)
			defer c.Close()
			note, err := c.AddNote("The rollback owner is Mira.")
			if err != nil {
				t.Fatal(err)
			}
			if err := c.ConfigureLocalAI(context.Background(), LocalAISettings{Enabled: true, Provider: ProviderOllama, Endpoint: server.URL, Model: "fixture"}); err != nil {
				t.Fatal(err)
			}
			app := &pausedAskApp{posted: make(chan func(), 32)}
			d := &desktop{app: app, client: c, captureState: "paused", askQuestion: "Who is Mira?", quickInput: "Who is Mira?"}
			if surface == "dashboard" {
				d.askMemory()
			} else {
				d.submitQuickPanelInput()
			}
			pump := func() {
				t.Helper()
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
				for d.loading || d.askLoading {
					select {
					case publish := <-app.posted:
						publish()
					case <-deadline.C:
						t.Fatalf("%s stalled: %q", surface, d.status)
					}
				}
				if d.askResult == nil || d.status != "" {
					t.Fatalf("%s did not publish an answer: %q", surface, d.status)
				}
			}
			pump()
			if d.askResult.General || len(d.askResult.Answer.SourceIDs) != 1 || d.askResult.Answer.SourceIDs[0] != note.SourceID {
				t.Fatalf("%s did not ground the initial named question in the saved note: %+v", surface, d.askResult)
			}
			if surface == "popup to dashboard" {
				d.openDashboard()
			}
			if surface == "popup" {
				d.quickInput = "Make it shorter"
				d.submitQuickPanelInput()
			} else {
				d.askQuestion = "Make it shorter"
				d.askMemory()
			}
			pump()
			answer := d.askResult
			if answer.General || answer.Direct || answer.ConversationTurns != 1 || answer.Answer.InsufficientEvidence || len(answer.Answer.SourceIDs) != 1 || answer.Answer.SourceIDs[0] != note.SourceID {
				t.Fatalf("%s lost grounded conversation: %+v", surface, answer)
			}
			mu.Lock()
			gotPayloads := append([]string(nil), payloads...)
			mu.Unlock()
			if len(gotPayloads) != 2 || !strings.Contains(gotPayloads[1], `"conversation"`) || !strings.Contains(gotPayloads[1], "The rollback owner is Mira.") || !strings.Contains(gotPayloads[1], "Who is Mira?") {
				t.Fatalf("%s lost the original subject/evidence at the provider boundary: %q", surface, gotPayloads)
			}
			page, err := c.SearchMemoryPageContext(context.Background(), "Make it shorter")
			if err != nil || len(page.Hits) != 0 {
				t.Fatalf("%s saved the continuation as a note: %+v %v", surface, page, err)
			}
			d.invalidateMemoryViews()
			if len(d.askConversation) != 0 || len(d.quickConversation) != 0 || d.askResult != nil {
				t.Fatalf("%s retained private conversation after invalidation", surface)
			}
		})
	}
}
