package assistant

import (
	"context"

	"github.com/hazcod/blur/internal/model"
)

// OpenAICompatibleGenerator adapts the provider-neutral Ask contract to an
// explicitly configured compatible endpoint. Routing and user consent remain
// the caller's responsibility; this adapter never selects a provider itself.
type OpenAICompatibleGenerator struct{ client *model.OpenAICompatible }

func NewOpenAICompatibleGenerator(endpoint, modelName, credential string) (*OpenAICompatibleGenerator, error) {
	client, err := model.NewOpenAICompatible(endpoint, modelName, credential)
	if err != nil {
		return nil, ErrAskUnavailable
	}
	return &OpenAICompatibleGenerator{client: client}, nil
}

func (g *OpenAICompatibleGenerator) CloseIdleConnections() {
	if g != nil && g.client != nil {
		g.client.CloseIdleConnections()
	}
}

func (g *OpenAICompatibleGenerator) CountInputTokens(ctx context.Context, system, user string) (int, error) {
	if g == nil || g.client == nil {
		return 0, ErrAskUnavailable
	}
	return g.client.CountInputTokens(ctx, system, user)
}

func (g *OpenAICompatibleGenerator) Generate(ctx context.Context, packed PackedContext, maxOutputTokens int) (ModelAnswer, error) {
	if g == nil || g.client == nil || ctx == nil || ctx.Err() != nil || maxOutputTokens < 1 || maxOutputTokens > MaxAnswerTokens || packed.System == "" || packed.User == "" || len(packed.SourceIDs) == 0 || len(packed.SourceIDs) > MaxEvidenceItems || packed.InputBytes != len(packed.System)+len(packed.User) || packed.InputBytes+maxOutputTokens+PromptTemplateReserve > ContextTokenLimit || !validPackedSources(packed.SourceIDs) {
		if ctx != nil && ctx.Err() != nil {
			return ModelAnswer{}, ctx.Err()
		}
		return ModelAnswer{}, ErrContext
	}
	content, err := g.client.Generate(ctx, packed.System, packed.User, maxOutputTokens)
	if err != nil {
		return ModelAnswer{}, err
	}
	return parseGeneratedAnswer(content, packed.SourceIDs)
}
