package model

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// InstalledModel describes verified local capabilities. Discovery reads model
// metadata only: it never downloads a model or submits a prompt.
type InstalledModel struct {
	Name       string
	Completion bool
	Embedding  bool
}

func DiscoverInstalledModels(ctx context.Context, endpoint string) ([]InstalledModel, error) {
	if ctx == nil {
		return nil, ErrOllamaConfig
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, err := NewOllama(endpoint, "discovery")
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	body, err := client.embeddingAPI(ctx, http.MethodGet, "/api/tags", nil, 256<<10)
	if err != nil {
		return nil, err
	}
	defer clear(body)
	var catalog struct {
		Models []struct {
			Name, Model, Digest string
			RemoteHost          string `json:"remote_host"`
			RemoteModel         string `json:"remote_model"`
		} `json:"models"`
	}
	if decodeOllamaResponse(body, &catalog) != nil || len(catalog.Models) > maxOllamaModelList {
		return nil, ErrOllamaResponse
	}
	seen := map[string]bool{}
	models := make([]InstalledModel, 0, len(catalog.Models))
	for _, item := range catalog.Models {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := item.Name
		if name == "" {
			name = item.Model
		}
		if item.RemoteHost != "" || item.RemoteModel != "" || strings.HasSuffix(strings.TrimSuffix(name, ":latest"), "-cloud") || strings.HasSuffix(name, ":cloud") {
			continue
		}
		if !validOllamaModelDigest(strings.TrimPrefix(item.Digest, "sha256:")) || seen[canonicalOllamaModelName(name)] {
			return nil, ErrOllamaResponse
		}
		probe, err := NewOllama(endpoint, name)
		if err != nil {
			return nil, ErrOllamaResponse
		}
		probe.CloseIdleConnections()
		seen[canonicalOllamaModelName(name)] = true
		encoded, _ := json.Marshal(struct {
			Model string `json:"model"`
		}{name})
		detailsBody, err := client.embeddingAPI(ctx, http.MethodPost, "/api/show", encoded, maxEmbeddingResponse)
		clear(encoded)
		if err != nil {
			return nil, err
		}
		var details struct {
			Capabilities []string `json:"capabilities"`
			RemoteHost   string   `json:"remote_host"`
			RemoteModel  string   `json:"remote_model"`
		}
		err = decodeOllamaResponse(detailsBody, &details)
		clear(detailsBody)
		if err != nil || len(details.Capabilities) > 32 {
			return nil, ErrOllamaResponse
		}
		if details.RemoteHost != "" || details.RemoteModel != "" {
			continue
		}
		found := InstalledModel{Name: name}
		for _, capability := range details.Capabilities {
			switch capability {
			case "completion":
				found.Completion = true
			case "embedding":
				found.Embedding = true
			}
		}
		if found.Completion || found.Embedding {
			models = append(models, found)
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models, nil
}
