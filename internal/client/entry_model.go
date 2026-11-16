package client

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/model"
)

// beginEntryParser borrows the same selected model as Ask. Snapshotting and
// cancellation registration share the policy lock, so retiring a provider or
// revoking saved-memory consent cannot leave an actionable late interpretation.
// Targets are saved memory; cloud interpretation therefore requires Ask consent.
func (c *Client) beginEntryParser(ctx context.Context, epoch uint64) (*intent.Ollama, context.Context, func(), error) {
	c.aiPolicyMu.RLock()
	defer c.aiPolicyMu.RUnlock()
	if c.localAI == nil {
		return nil, ctx, func() {}, nil
	}
	c.localAI.mu.RLock()
	settings, selected, repair := c.localAI.settings, c.localAI.askModel, c.localAI.needsRepair
	c.localAI.mu.RUnlock()
	provider := effectiveProvider(settings)
	remoteAllowed := isRemoteCompletionProvider(provider) && settings.RemoteAskEnabled
	if !settings.Enabled || repair || selected == nil || !isLocalCompletionProvider(provider) && !remoteAllowed {
		return nil, ctx, func() {}, nil
	}
	generator, ok := selected.(assistant.PreparedGeneralGenerator)
	if !ok {
		return nil, ctx, func() {}, nil
	}
	parseCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	id, ok := c.registerAskEpoch(cancel, &epoch)
	if !ok {
		cancel()
		return nil, nil, nil, ErrMemoryChanging
	}
	parser, err := intent.NewCompletion(entryCompletion{selected, generator}, provider == ProviderApple)
	if err != nil {
		cancel()
		c.unregisterAsk(id)
		return nil, nil, nil, err
	}
	return parser, parseCtx, func() { cancel(); c.unregisterAsk(id) }, nil
}

type entryCompletion struct {
	selected  configuredAskModel
	generator assistant.PreparedGeneralGenerator
}

func (g entryCompletion) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	var text string
	var err error
	if structured, ok := g.selected.(assistant.PreparedJSONGenerator); ok && len(request.Schema) > 0 {
		// The structured provider already counts the full request/schema
		// against its actual runtime capacity. Avoid a second native-tokenizer
		// round trip for each kind/detail request.
		text, err = structured.GenerateGeneralJSONPrompt(ctx, request.System, request.User, request.Schema, request.MaxOutputTokens)
	} else {
		tokens, countErr := g.selected.CountInputTokens(ctx, request.System, request.User)
		if ctx.Err() != nil {
			return model.Response{}, ctx.Err()
		}
		if countErr != nil {
			return model.Response{}, countErr
		}
		if tokens < 0 || tokens+len(request.Schema)+request.MaxOutputTokens+assistant.PromptTemplateReserve > assistant.ContextTokenLimit {
			return model.Response{}, assistant.ErrTokenBudget
		}
		text, err = g.generator.GenerateGeneralPrompt(ctx, request.System, request.User, request.MaxOutputTokens)
	}
	if ctx.Err() != nil {
		return model.Response{}, ctx.Err()
	}
	return model.Response{Text: text}, err
}

// The controller owns the borrowed provider's lifetime.
func (entryCompletion) CloseIdleConnections() {}
