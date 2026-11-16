package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscoverInstalledModelsReadsMetadataAndFiltersRemote(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{
				map[string]string{"name": "answers:latest", "digest": strings.Repeat("a", 64)},
				map[string]string{"name": "embeddinggemma:latest", "digest": strings.Repeat("b", 64)},
				map[string]string{"name": "hosted:cloud", "digest": strings.Repeat("c", 64)},
				map[string]string{"name": "hidden:latest", "digest": strings.Repeat("d", 64)},
			}})
		case "/api/show":
			var input struct {
				Model string `json:"model"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				t.Error("invalid metadata request")
			}
			switch input.Model {
			case "answers:latest":
				_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "tools"}})
			case "embeddinggemma:latest":
				_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"embedding"}})
			case "hidden:latest":
				_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion"}, "remote_host": "remote.example"})
			default:
				t.Error("cloud model metadata admitted")
				w.WriteHeader(400)
			}
		default:
			t.Error("discovery performed inference")
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	models, err := DiscoverInstalledModels(context.Background(), server.URL)
	if err != nil || len(models) != 2 || requests != 4 || models[0].Name != "answers:latest" || !models[0].Completion || models[0].Embedding || models[1].Name != "embeddinggemma:latest" || !models[1].Embedding || models[1].Completion {
		t.Fatalf("discovered=%+v requests=%d err=%v", models, requests, err)
	}
}

func TestDiscoverInstalledModelsRejectsRemoteEndpointAndCancellation(t *testing.T) {
	if _, err := DiscoverInstalledModels(context.Background(), "https://remote.example"); err == nil {
		t.Fatal("discovery accepted nonlocal server")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DiscoverInstalledModels(ctx, "http://127.0.0.1:11434"); err != context.Canceled {
		t.Fatalf("cancellation=%v", err)
	}
}
