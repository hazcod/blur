package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"
)

const (
	MaxEmbeddingInputs     = 16
	MaxEmbeddingItemBytes  = 8 << 10
	MaxEmbeddingInputBytes = 16 << 10
	MaxEmbeddingDimensions = 2048
	maxEmbeddingRequest    = 128 << 10
	maxEmbeddingResponse   = 2 << 20
	maxOllamaModelList     = 256
)

var (
	ErrOllamaRemoteModel   = errors.New("model: local inference required")
	ErrOllamaModelMissing  = errors.New("model: selected Ollama model is not installed")
	ErrOllamaModelChanged  = errors.New("model: embedding model identity changed")
	ErrOllamaNotEmbedding  = errors.New("model: selected model does not support embeddings")
	ErrOllamaNotCompletion = errors.New("model: selected model does not support answers")
)

// EmbeddingModel binds vectors to the installed model manifest, not a mutable
// model name. Callers include Digest in their index generation identity.
type EmbeddingModel struct {
	Name   string
	Digest string
}

type EmbeddingResponse struct {
	Vectors     [][]float32
	Dimensions  int
	InputTokens int
}

// ResolveEmbeddingModel sends model metadata only. It refuses Ollama cloud
// forwarding before any captured evidence can enter an embedding request.
func (o *Ollama) ResolveEmbeddingModel(ctx context.Context) (EmbeddingModel, error) {
	return o.resolveLocalModel(ctx, "embedding")
}

// ResolveCompletionModel verifies the local manifest and rejects Ollama cloud
// forwarding before a question, note, or captured source is transmitted.
func (o *Ollama) ResolveCompletionModel(ctx context.Context) (EmbeddingModel, error) {
	return o.resolveLocalModel(ctx, "completion")
}

func (o *Ollama) resolveLocalModel(ctx context.Context, required string) (EmbeddingModel, error) {
	identity, err := o.readEmbeddingModelIdentity(ctx)
	if err != nil {
		return EmbeddingModel{}, err
	}
	encoded, err := json.Marshal(struct {
		Model   string `json:"model"`
		Verbose bool   `json:"verbose"`
	}{Model: identity.Name})
	if err != nil {
		return EmbeddingModel{}, ErrOllamaConfig
	}
	defer clear(encoded)
	body, err := o.embeddingAPI(ctx, http.MethodPost, "/api/show", encoded, maxEmbeddingResponse)
	if err != nil {
		return EmbeddingModel{}, err
	}
	defer clear(body)
	var details struct {
		Capabilities []string `json:"capabilities"`
		RemoteHost   string   `json:"remote_host"`
		RemoteModel  string   `json:"remote_model"`
	}
	if decodeOllamaResponse(body, &details) != nil || len(details.Capabilities) > 32 {
		return EmbeddingModel{}, ErrOllamaResponse
	}
	if details.RemoteHost != "" || details.RemoteModel != "" {
		return EmbeddingModel{}, ErrOllamaRemoteModel
	}
	for _, capability := range details.Capabilities {
		if capability == required {
			return identity, nil
		}
	}
	if required == "embedding" {
		return EmbeddingModel{}, ErrOllamaNotEmbedding
	}
	return EmbeddingModel{}, ErrOllamaNotCompletion
}

func (o *Ollama) VerifyLocalModel(ctx context.Context, expected EmbeddingModel) error {
	identity, err := o.readEmbeddingModelIdentity(ctx)
	if err != nil {
		return err
	}
	if identity != expected {
		return ErrOllamaModelChanged
	}
	return nil
}

// Embed uses the same loopback-only, proxy-free transport as the answer path.
// Input is never silently truncated. Model identity is checked before and
// after inference; a replaced model cannot contaminate an existing index.
func (o *Ollama) Embed(ctx context.Context, expected EmbeddingModel, input []string, dimensions int) (EmbeddingResponse, error) {
	if o == nil || o.client == nil || ctx == nil || len(input) < 1 || len(input) > MaxEmbeddingInputs || dimensions < 0 || dimensions > MaxEmbeddingDimensions || expected.Name != canonicalOllamaModelName(o.model) || !validOllamaModelDigest(expected.Digest) {
		return EmbeddingResponse{}, ErrOllamaConfig
	}
	if err := ctx.Err(); err != nil {
		return EmbeddingResponse{}, err
	}
	inputBytes := 0
	for _, text := range input {
		if len(text) == 0 || len(text) > MaxEmbeddingItemBytes || strings.TrimSpace(text) == "" || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return EmbeddingResponse{}, ErrOllamaBudget
		}
		inputBytes += len(text)
		if inputBytes > MaxEmbeddingInputBytes {
			return EmbeddingResponse{}, ErrOllamaBudget
		}
	}
	identity, err := o.ResolveEmbeddingModel(ctx)
	if err != nil {
		return EmbeddingResponse{}, err
	}
	if identity != expected {
		return EmbeddingResponse{}, ErrOllamaModelChanged
	}
	encoded, err := json.Marshal(struct {
		Model      string   `json:"model"`
		Input      []string `json:"input"`
		Truncate   bool     `json:"truncate"`
		Dimensions int      `json:"dimensions,omitempty"`
		KeepAlive  string   `json:"keep_alive"`
	}{Model: identity.Name, Input: input, Dimensions: dimensions, KeepAlive: "30s"})
	if err != nil || len(encoded) > maxEmbeddingRequest {
		clear(encoded)
		return EmbeddingResponse{}, ErrOllamaBudget
	}
	defer clear(encoded)
	body, err := o.embeddingAPI(ctx, http.MethodPost, "/api/embed", encoded, maxEmbeddingResponse)
	if err != nil {
		return EmbeddingResponse{}, err
	}
	defer clear(body)
	var response struct {
		Model           string      `json:"model"`
		Embeddings      [][]float32 `json:"embeddings"`
		PromptEvalCount int         `json:"prompt_eval_count"`
		RemoteHost      string      `json:"remote_host"`
		RemoteModel     string      `json:"remote_model"`
	}
	if decodeOllamaResponse(body, &response) != nil || canonicalOllamaModelName(response.Model) != expected.Name || len(response.Embeddings) != len(input) || response.PromptEvalCount < 0 || response.PromptEvalCount > MaxEmbeddingInputBytes*2 {
		return EmbeddingResponse{}, ErrOllamaResponse
	}
	if response.RemoteHost != "" || response.RemoteModel != "" {
		return EmbeddingResponse{}, ErrOllamaRemoteModel
	}
	width := len(response.Embeddings[0])
	if width < 1 || width > MaxEmbeddingDimensions || dimensions > 0 && width != dimensions {
		return EmbeddingResponse{}, ErrOllamaResponse
	}
	for _, vector := range response.Embeddings {
		if len(vector) != width {
			return EmbeddingResponse{}, ErrOllamaResponse
		}
		var squared float64
		for _, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return EmbeddingResponse{}, ErrOllamaResponse
			}
			squared += float64(value) * float64(value)
		}
		if squared == 0 || math.IsNaN(squared) || math.IsInf(squared, 0) {
			return EmbeddingResponse{}, ErrOllamaResponse
		}
		length := math.Sqrt(squared)
		for index := range vector {
			vector[index] = float32(float64(vector[index]) / length)
		}
	}
	identity, err = o.readEmbeddingModelIdentity(ctx)
	if err != nil {
		return EmbeddingResponse{}, err
	}
	if identity != expected {
		return EmbeddingResponse{}, ErrOllamaModelChanged
	}
	return EmbeddingResponse{Vectors: response.Embeddings, Dimensions: width, InputTokens: response.PromptEvalCount}, nil
}

func (o *Ollama) readEmbeddingModelIdentity(ctx context.Context) (EmbeddingModel, error) {
	if o == nil || o.client == nil || ctx == nil {
		return EmbeddingModel{}, ErrOllamaConfig
	}
	if err := ctx.Err(); err != nil {
		return EmbeddingModel{}, err
	}
	body, err := o.embeddingAPI(ctx, http.MethodGet, "/api/tags", nil, 256<<10)
	if err != nil {
		return EmbeddingModel{}, err
	}
	defer clear(body)
	var response struct {
		Models []struct {
			Name        string `json:"name"`
			Model       string `json:"model"`
			Digest      string `json:"digest"`
			RemoteHost  string `json:"remote_host"`
			RemoteModel string `json:"remote_model"`
		} `json:"models"`
	}
	if decodeOllamaResponse(body, &response) != nil || len(response.Models) > maxOllamaModelList {
		return EmbeddingModel{}, ErrOllamaResponse
	}
	want := canonicalOllamaModelName(o.model)
	var selected EmbeddingModel
	for _, item := range response.Models {
		name := item.Name
		if name == "" {
			name = item.Model
		}
		if canonicalOllamaModelName(name) != want {
			continue
		}
		if item.RemoteHost != "" || item.RemoteModel != "" || strings.HasSuffix(strings.TrimSuffix(name, ":latest"), "-cloud") || strings.HasSuffix(name, ":cloud") {
			return EmbeddingModel{}, ErrOllamaRemoteModel
		}
		digest := strings.TrimPrefix(item.Digest, "sha256:")
		if !validOllamaModelDigest(digest) || selected.Name != "" {
			return EmbeddingModel{}, ErrOllamaResponse
		}
		selected = EmbeddingModel{Name: want, Digest: digest}
	}
	if selected.Name == "" {
		return EmbeddingModel{}, ErrOllamaModelMissing
	}
	return selected, nil
}

func (o *Ollama) embeddingAPI(ctx context.Context, method, path string, body []byte, maxBytes int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, o.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, ErrOllamaConfig
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := o.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrOllamaUnavailable
	}
	defer func(close func() error) { _ = close() }(response.Body.Close)
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusNotFound && path != "/api/tags" {
			return nil, ErrOllamaModelMissing
		}
		return nil, ErrOllamaUnavailable
	}
	output, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil || int64(len(output)) > maxBytes {
		clear(output)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrOllamaResponse
	}
	return output, nil
}

func validOllamaModelDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func canonicalOllamaModelName(name string) string {
	if name == "" || len(name) > 128 || !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\r\n ") {
		return ""
	}
	if strings.LastIndexByte(name, ':') < strings.LastIndexByte(name, '/') || !strings.ContainsRune(name, ':') {
		return name + ":latest"
	}
	return name
}
