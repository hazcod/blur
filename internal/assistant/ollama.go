package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/hazcod/blur/internal/model"
)

var ErrGeneratorResponse = errors.New("assistant: invalid generated answer")

type OllamaGenerator struct{ client *model.Ollama }

func NewOllamaGenerator(endpoint, modelName string) (*OllamaGenerator, error) {
	client, err := model.NewOllama(endpoint, modelName)
	if err != nil {
		return nil, err
	}
	return &OllamaGenerator{client: client}, nil
}

func (g *OllamaGenerator) CloseIdleConnections() {
	if g != nil && g.client != nil {
		g.client.CloseIdleConnections()
	}
}

func (g *OllamaGenerator) CountInputTokens(ctx context.Context, system, user string) (int, error) {
	if g == nil || g.client == nil {
		return 0, ErrAskUnavailable
	}
	return g.client.CountInputTokens(ctx, system, user)
}

func (g *OllamaGenerator) Generate(ctx context.Context, packed PackedContext, maxOutputTokens int) (ModelAnswer, error) {
	if g == nil || g.client == nil || ctx == nil || ctx.Err() != nil || maxOutputTokens < 1 || maxOutputTokens > MaxAnswerTokens || packed.System == "" || packed.User == "" || len(packed.SourceIDs) == 0 || len(packed.SourceIDs) > MaxEvidenceItems || packed.InputBytes != len(packed.System)+len(packed.User) || packed.InputBytes+maxOutputTokens+PromptTemplateReserve > ContextTokenLimit || !validPackedSources(packed.SourceIDs) {
		if ctx != nil && ctx.Err() != nil {
			return ModelAnswer{}, ctx.Err()
		}
		return ModelAnswer{}, ErrContext
	}
	response, err := g.client.Generate(ctx, model.Request{System: packed.System, User: packed.User, Format: "json", MaxOutputTokens: maxOutputTokens})
	if err != nil {
		return ModelAnswer{}, err
	}
	return parseGeneratedAnswer(response.Text, packed.SourceIDs)
}

func parseGeneratedAnswer(content string, sourceIDs []string) (ModelAnswer, error) {
	var answer ModelAnswer
	decoder := json.NewDecoder(bytes.NewBufferString(content))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&answer) != nil {
		return ModelAnswer{}, ErrGeneratorResponse
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || strings.TrimSpace(answer.Text) == "" && !answer.InsufficientEvidence {
		return ModelAnswer{}, ErrGeneratorResponse
	}
	resolvedSourceIDs, err := resolveModelSourceLabels(answer.SourceIDs, sourceIDs)
	if err != nil {
		return ModelAnswer{}, ErrGeneratorResponse
	}
	answer.SourceIDs = resolvedSourceIDs
	validated, err := ValidateAnswer(answer, sourceIDs)
	if err != nil {
		return ModelAnswer{}, err
	}
	return ModelAnswer{Text: validated.Text, SourceIDs: validated.SourceIDs, InsufficientEvidence: validated.InsufficientEvidence}, nil
}

func resolveModelSourceLabels(labels, allowedSourceIDs []string) ([]string, error) {
	if len(labels) > MaxEvidenceItems || len(allowedSourceIDs) > MaxEvidenceItems {
		return nil, ErrAnswer
	}
	allowed := make(map[string]struct{}, len(allowedSourceIDs))
	for _, id := range allowedSourceIDs {
		if !validSourceID(id) {
			return nil, ErrAnswer
		}
		allowed[id] = struct{}{}
	}
	resolved := make([]string, 0, len(labels))
	for _, label := range labels {
		if _, ok := allowed[label]; ok { // Accept the older full-ID format as well.
			resolved = append(resolved, label)
			continue
		}
		if len(label) != 2 || label[0] != 'S' || label[1] < '1' || label[1] > '8' {
			return nil, ErrAnswer
		}
		index := int(label[1] - '0')
		if index == 0 || index > len(allowedSourceIDs) {
			return nil, ErrAnswer
		}
		resolved = append(resolved, allowedSourceIDs[index-1])
	}
	return resolved, nil
}

func validPackedSources(ids []string) bool {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !validSourceID(id) {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}
