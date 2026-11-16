package semantic

import (
	"context"
	"strings"

	"github.com/hazcod/blur/internal/model"
)

const DefaultEmbeddingModel = "embeddinggemma"

type OllamaEmbedder struct {
	client     *model.Ollama
	dimensions int
}

func NewOllamaEmbedder(endpoint, name string, dimensions int) (*OllamaEmbedder, error) {
	if dimensions < 1 || dimensions > MaxDimensions {
		return nil, ErrInvalid
	}
	client, err := model.NewOllama(endpoint, name)
	if err != nil {
		return nil, err
	}
	return &OllamaEmbedder{client: client, dimensions: dimensions}, nil
}

func (e *OllamaEmbedder) CloseIdleConnections() {
	if e != nil && e.client != nil {
		e.client.CloseIdleConnections()
	}
}

func (e *OllamaEmbedder) Identity(ctx context.Context) (Model, error) {
	if e == nil || e.client == nil || ctx == nil {
		return Model{}, ErrInvalid
	}
	identity, err := e.client.ResolveEmbeddingModel(ctx)
	if err != nil {
		return Model{}, err
	}
	return Model{Name: identity.Name, Digest: identity.Digest, Dimensions: e.dimensions, Format: embeddingFormat(identity.Name)}, nil
}

func (e *OllamaEmbedder) EmbedDocuments(ctx context.Context, identity Model, texts []string) ([][]float32, error) {
	if !e.compatible(identity) {
		return nil, ErrModelMismatch
	}
	input := make([]string, len(texts))
	for index, text := range texts {
		input[index] = documentEmbeddingText(identity.Format, text)
	}
	result, err := e.client.Embed(ctx, model.EmbeddingModel{Name: identity.Name, Digest: identity.Digest}, input, identity.Dimensions)
	if err != nil {
		return nil, err
	}
	return result.Vectors, nil
}

func (e *OllamaEmbedder) EmbedQuery(ctx context.Context, identity Model, text string) ([]float32, error) {
	if !e.compatible(identity) {
		return nil, ErrModelMismatch
	}
	input := text
	switch identity.Format {
	case FormatGemma:
		input = "task: search result | query: " + text
	case FormatNomic:
		input = "search_query: " + text
	}
	result, err := e.client.Embed(ctx, model.EmbeddingModel{Name: identity.Name, Digest: identity.Digest}, []string{input}, identity.Dimensions)
	if err != nil {
		return nil, err
	}
	return result.Vectors[0], nil
}

func (e *OllamaEmbedder) compatible(identity Model) bool {
	return e != nil && e.client != nil && identity.Validate() == nil && identity.Dimensions == e.dimensions && identity.Format == embeddingFormat(identity.Name)
}

func documentEmbeddingText(format, text string) string {
	switch format {
	case FormatGemma:
		return "title: none | text: " + text
	case FormatNomic:
		return "search_document: " + text
	default:
		return text
	}
}

func embeddingFormat(name string) string {
	name = strings.ToLower(name)
	if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
		name = name[slash+1:]
	}
	switch {
	case strings.HasPrefix(name, "embeddinggemma"):
		return FormatGemma
	case strings.HasPrefix(name, "nomic-embed-text"):
		return FormatNomic
	default:
		return FormatPlain
	}
}
