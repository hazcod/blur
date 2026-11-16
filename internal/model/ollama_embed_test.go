package model

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOllamaEmbeddingsResolveLocalModelAndNormalizeBoundedBatch(t *testing.T) {
	digest := strings.Repeat("a", 64)
	var embedCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "memory-embed:latest", "digest": digest}}})
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"embedding"}})
		case "/api/embed":
			embedCalls.Add(1)
			var request struct {
				Model      string   `json:"model"`
				Input      []string `json:"input"`
				Truncate   *bool    `json:"truncate"`
				Dimensions int      `json:"dimensions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "memory-embed:latest" || len(request.Input) != 2 || request.Truncate == nil || *request.Truncate || request.Dimensions != 2 {
				t.Errorf("embedding request violated the batch/model/truncation contract: %+v err=%v", request, err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "memory-embed:latest", "embeddings": [][]float32{{3, 4}, {0, 2}}, "prompt_eval_count": 12})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewOllama(server.URL, "memory-embed")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	identity, err := client.ResolveEmbeddingModel(context.Background())
	if err != nil || identity.Name != "memory-embed:latest" || identity.Digest != digest {
		t.Fatalf("model identity = %+v err=%v", identity, err)
	}
	result, err := client.Embed(context.Background(), identity, []string{"A source paragraph", "A second source"}, 2)
	if err != nil || embedCalls.Load() != 1 || result.Dimensions != 2 || len(result.Vectors) != 2 || math.Abs(float64(result.Vectors[0][0])-0.6) > 1e-6 || math.Abs(float64(result.Vectors[0][1])-0.8) > 1e-6 || result.Vectors[1][1] != 1 {
		t.Fatalf("embeddings = %+v err=%v calls=%d", result, err, embedCalls.Load())
	}
}

func TestOllamaEmbeddingResolutionRejectsCloudModelsBeforeSendingEvidence(t *testing.T) {
	var embedCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "memory-embed:latest", "digest": strings.Repeat("a", 64)}}})
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"embedding"}, "remote_host": "https://cloud.example", "remote_model": "upstream"})
		case "/api/embed":
			embedCalls.Add(1)
			http.Error(w, "must not receive evidence", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	client, _ := NewOllama(server.URL, "memory-embed")
	defer client.CloseIdleConnections()
	if _, err := client.ResolveEmbeddingModel(context.Background()); !errors.Is(err, ErrOllamaRemoteModel) || embedCalls.Load() != 0 {
		t.Fatalf("cloud model resolution error=%v embed calls=%d", err, embedCalls.Load())
	}
}

func TestOllamaEmbeddingsRejectChangedModelsAndMalformedVectors(t *testing.T) {
	for _, tc := range []struct {
		name, body                              string
		beforeChange, afterChange, notEmbedding bool
		want                                    error
		calls                                   int32
	}{
		{name: "changed before inference", beforeChange: true, want: ErrOllamaModelChanged},
		{name: "changed after inference", afterChange: true, body: `{"model":"memory-embed:latest","embeddings":[[1,0]]}`, want: ErrOllamaModelChanged, calls: 1},
		{name: "answer model", notEmbedding: true, want: ErrOllamaNotEmbedding},
		{name: "zero vector", body: `{"model":"memory-embed:latest","embeddings":[[0,0]]}`, want: ErrOllamaResponse, calls: 1},
		{name: "wrong dimensions", body: `{"model":"memory-embed:latest","embeddings":[[1]]}`, want: ErrOllamaResponse, calls: 1},
		{name: "missing vector", body: `{"model":"memory-embed:latest","embeddings":[]}`, want: ErrOllamaResponse, calls: 1},
		{name: "nonfinite vector", body: `{"model":"memory-embed:latest","embeddings":[[1e1000,0]]}`, want: ErrOllamaResponse, calls: 1},
		{name: "trailing data", body: `{"model":"memory-embed:latest","embeddings":[[1,0]]} {}`, want: ErrOllamaResponse, calls: 1},
		{name: "remote response", body: `{"model":"memory-embed:latest","embeddings":[[1,0]],"remote_host":"https://cloud.example"}`, want: ErrOllamaRemoteModel, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tags, calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/tags":
					n := tags.Add(1)
					digest := strings.Repeat("a", 64)
					if tc.beforeChange || tc.afterChange && n > 1 {
						digest = strings.Repeat("b", 64)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "memory-embed:latest", "digest": digest}}})
				case "/api/show":
					capability := "embedding"
					if tc.notEmbedding {
						capability = "completion"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{capability}})
				case "/api/embed":
					calls.Add(1)
					_, _ = w.Write([]byte(tc.body))
				}
			}))
			defer server.Close()
			client, _ := NewOllama(server.URL, "memory-embed")
			defer client.CloseIdleConnections()
			_, err := client.Embed(context.Background(), EmbeddingModel{Name: "memory-embed:latest", Digest: strings.Repeat("a", 64)}, []string{"Synthetic source"}, 2)
			if !errors.Is(err, tc.want) || calls.Load() != tc.calls {
				t.Fatalf("err=%v calls=%d want err=%v calls=%d", err, calls.Load(), tc.want, tc.calls)
			}
		})
	}
}

func TestOllamaEmbeddingsRejectOversizedInputBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client, _ := NewOllama(server.URL, "memory-embed")
	defer client.CloseIdleConnections()
	_, err := client.Embed(context.Background(), EmbeddingModel{Name: "memory-embed:latest", Digest: strings.Repeat("a", 64)}, []string{strings.Repeat("x", MaxEmbeddingItemBytes+1)}, 2)
	if !errors.Is(err, ErrOllamaBudget) || calls.Load() != 0 {
		t.Fatalf("oversized input reached provider: err=%v calls=%d", err, calls.Load())
	}
}
