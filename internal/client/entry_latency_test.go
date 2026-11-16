package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/model/modeltest"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestClearQuickQuestionsSkipIntentModel(t *testing.T) {
	var generations atomic.Int32
	server := httptest.NewServer(modeltest.LocalOllama("fixture", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		generations.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "message": map[string]string{"content": `{"kind":"clarify","text":"Unexpected intent inference"}`}})
	})))
	defer server.Close()
	controller := newLocalAIController(filepath.Join(t.TempDir(), "ai.json"), notes.NewService(nil, nil, nil))
	c := NewClient(nil).WithLocalAI(controller)
	defer c.Close()
	defer controller.Close()
	if err := c.ConfigureLocalAI(context.Background(), LocalAISettings{Enabled: true, Endpoint: server.URL, Model: "fixture"}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"What or who is Mira?", "Who is Mira?", "What is SQLite?", "Which port does Aurora use?", "What did I do today?", "How much time did I spend in Slack?", "How do I implement a cache?", "what integrity emails were we talking about?"} {
		t.Run(input, func(t *testing.T) {
			generations.Store(0)
			resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
			if err != nil || resolved.Action.Kind != intent.Ask || resolved.Action.Text != input || resolved.Action.Mode != "" || generations.Load() != 0 {
				t.Fatalf("clear question waited for intent inference: result=%+v calls=%d err=%v", resolved, generations.Load(), err)
			}
		})
	}
	for _, input := range []string{"Could you schedule a reminder for Friday at nine am to check backups?", "Can you save the important parts of my last conversation?", "Please add reviewing the PR to my tasks"} {
		t.Run(input, func(t *testing.T) {
			generations.Store(0)
			resolved, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
			if err != nil || resolved.Action.Kind != intent.Clarify || generations.Load() != 1 {
				t.Fatalf("ambiguous/action request bypassed interpretation: result=%+v calls=%d err=%v", resolved, generations.Load(), err)
			}
		})
	}
}

// Optional timing probe uses only a temporary encrypted synthetic note and the
// installed loopback model. It never opens the user's memory or settings.
func TestLocalQuickQuestionUsesOneModelRequest(t *testing.T) {
	if os.Getenv("BLUR_LOCAL_MODEL_INTEGRATION") != "1" {
		t.Skip("requires an installed local answer model")
	}
	upstream, _ := url.Parse("http://127.0.0.1:11434")
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.Request.URL.Path != "/api/chat" {
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
		_ = response.Body.Close()
		if err != nil {
			return err
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		var metrics struct {
			TotalDuration      int64 `json:"total_duration"`
			LoadDuration       int64 `json:"load_duration"`
			PromptEvalDuration int64 `json:"prompt_eval_duration"`
			EvalDuration       int64 `json:"eval_duration"`
			PromptEvalCount    int   `json:"prompt_eval_count"`
			EvalCount          int   `json:"eval_count"`
		}
		if err := json.Unmarshal(body, &metrics); err != nil {
			return err
		}
		t.Logf("provider phases: total=%s load=%s prompt=%s (%d tokens) decode=%s (%d tokens)", time.Duration(metrics.TotalDuration), time.Duration(metrics.LoadDuration), time.Duration(metrics.PromptEvalDuration), metrics.PromptEvalCount, time.Duration(metrics.EvalDuration), metrics.EvalCount)
		return nil
	}
	var generations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		var generation int32
		if r.URL.Path == "/api/chat" {
			generation = generations.Add(1)
		}
		proxy.ServeHTTP(w, r)
		t.Logf("provider HTTP %s generation=%d duration=%s", r.URL.Path, generation, time.Since(started))
	}))
	defer server.Close()
	dir := t.TempDir()
	store, err := durable.OpenDurable(filepath.Join(dir, "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	noteService := notes.NewService(store, nil, func() time.Time { return time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) })
	controller := newLocalAIController(filepath.Join(dir, "ai.json"), noteService)
	c := NewClient(store).WithNotes(noteService).WithLocalAI(controller)
	defer c.Close()
	defer controller.Close()
	if _, err := c.AddNote("Acceptance fixture: Aurora cache listens on port 4317. The rollback owner is Mira. This is synthetic test content."); err != nil {
		t.Fatal(err)
	}
	name := os.Getenv("BLUR_LOCAL_TEST_MODEL")
	if name == "" {
		name = "qwen3:14b"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := c.ConfigureLocalAI(ctx, LocalAISettings{Enabled: true, Endpoint: server.URL, Model: name}); err != nil {
		t.Fatal(err)
	}
	trials := 1
	if value := os.Getenv("BLUR_LOCAL_LATENCY_TRIALS"); value != "" {
		trials, err = strconv.Atoi(value)
		if err != nil || trials < 1 || trials > 9 {
			t.Fatal("BLUR_LOCAL_LATENCY_TRIALS must be 1..9")
		}
	}
	for trial := 0; trial < trials; trial++ {
		input := "What or who is Mira?"
		if trial%3 == 2 {
			input = "Who is the rollback owner?"
		}
		generations.Store(0)
		started := time.Now()
		resolved, err := c.ResolveEntry(ctx, input, EntryState{}, "UTC")
		interpreted := time.Now()
		if err != nil || resolved.Action.Kind != intent.Ask {
			t.Fatal(resolved, err)
		}
		answer, err := c.askMemoryWithMode(ctx, resolved.Action.Text, nil, resolved.Action.Mode)
		t.Logf("trial=%d interpretation=%s answer=%s total=%s model_requests=%d words=%d", trial+1, interpreted.Sub(started), time.Since(interpreted), time.Since(started), generations.Load(), len(strings.Fields(answer.Answer.Text)))
		if err != nil || answer.General || len(answer.Sources) != 1 || !strings.Contains(strings.ToLower(answer.Answer.Text), "mira") || !strings.Contains(strings.ToLower(answer.Answer.Text), "rollback") {
			t.Fatal("synthetic grounded answer failed", answer, err)
		}
		if generations.Load() != 1 {
			t.Fatalf("one clear question made %d model requests; want one grounded answer generation", generations.Load())
		}
		if words := len(strings.Fields(answer.Answer.Text)); words > 16 {
			t.Errorf("simple identity answer buried the fact in %d words; want a direct factual sentence", words)
		}
	}
}
