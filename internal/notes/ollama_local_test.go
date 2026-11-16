package notes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/model/modeltest"
)

func TestOllamaNoteRefusesCloudBeforeSendingNote(t *testing.T) {
	var generated atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "answer:latest", "digest": strings.Repeat("a", 64)}}})
		case "/api/show":
			_, _ = w.Write([]byte(`{"capabilities":["completion"],"remote_host":"https://cloud.example"}`))
		case "/api/generate":
			generated.Add(1)
		}
	}))
	defer server.Close()
	extractor, _ := NewOllamaExtractor(server.URL, "answer")
	defer extractor.CloseIdleConnections()
	note, _ := capture.SanitizeObservation("sh.blur.notes", "local", "Synthetic private note", 100)
	if _, err := extractor.Extract(context.Background(), note); !errors.Is(err, ErrModelUnavailable) || generated.Load() != 0 {
		t.Fatalf("cloud note request: err=%v calls=%d", err, generated.Load())
	}
}

func TestOllamaNoteAcceptsTransportMetadataButKeepsProposalSchemaStrict(t *testing.T) {
	server := httptest.NewServer(modeltest.LocalOllama("answer", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Think *bool `json:"think"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Think == nil || *request.Think {
			t.Error("note inference did not disable unbounded thinking")
		}
		_, _ = w.Write([]byte(`{"model":"answer:latest","done":true,"created_at":"2026-11-12T00:00:00Z","total_duration":123,"prompt_eval_count":100,"response":"{\"proposals\":[{\"kind\":\"task\",\"text\":\"Send the revised plan\"}]}"}`))
	})))
	defer server.Close()
	extractor, _ := NewOllamaExtractor(server.URL, "answer")
	defer extractor.CloseIdleConnections()
	note, _ := capture.SanitizeObservation("sh.blur.notes", "local", "Task: send the revised plan", 100)
	proposals, err := extractor.Extract(context.Background(), note)
	if err != nil || len(proposals) != 1 || proposals[0].Kind != Task {
		t.Fatalf("normal Ollama metadata rejected: %+v err=%v", proposals, err)
	}
}

func TestLocalOllamaNoteExtraction(t *testing.T) {
	if os.Getenv("BLUR_LOCAL_MODEL_INTEGRATION") != "1" {
		t.Skip("requires a configured real local answer model")
	}
	name := os.Getenv("BLUR_LOCAL_TEST_MODEL")
	if name == "" {
		name = "qwen3:14b"
	}
	extractor, err := NewOllamaExtractor("http://127.0.0.1:11434", name)
	if err != nil {
		t.Fatal(err)
	}
	defer extractor.CloseIdleConnections()
	note, _ := capture.SanitizeObservation("sh.blur.notes", "local", "Task: send the revised plan to Mina.", 200)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	proposals, err := extractor.Extract(ctx, note)
	if err != nil || len(proposals) == 0 {
		t.Fatalf("real local note extraction returned no proposal: %+v err=%v", proposals, err)
	}
	found := false
	for _, proposal := range proposals {
		if proposal.Kind == Task && strings.Contains(strings.ToLower(proposal.Text), "plan") {
			found = true
		}
	}
	if !found {
		t.Fatalf("explicit task was lost: %+v", proposals)
	}
}
