package model

import (
	"context"
	"errors"
)

var ErrLocalCompletionConfig = errors.New("model: invalid local completion configuration")

// LocalCompletion is the bounded completion port shared by on-device providers.
// Identity is provider-specific; Apple identifies its OS build and capability,
// while Ollama can bind jobs to an immutable local manifest digest.
type LocalCompletion interface {
	Generate(context.Context, Request) (Response, error)
	// CountInputTokens reports input cost, without output/template reserves or
	// padding for runtime capacity. Native tokenizers report actual tokens;
	// providers without a tokenizer may report a conservative estimate.
	CountInputTokens(context.Context, string, string) (int, error)
	CloseIdleConnections()
	Identity(context.Context) (string, error)
}

// TokenCapacity reports the complete runtime context independently of the
// request's actual token cost. Providers without this port use the caller limit.
type TokenCapacity interface {
	ContextTokenLimit(context.Context) (int, error)
}

// RequestTokenCounter includes format guidance and schemas used by Generate.
type RequestTokenCounter interface {
	CountRequestTokens(context.Context, Request) (int, error)
}

func RuntimeContextLimit(ctx context.Context, provider any, configuredLimit int) (int, error) {
	if ctx == nil || configuredLimit <= PromptTemplateReserve || configuredLimit > ContextTokenLimit {
		return 0, ErrLocalCompletionConfig
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	capacity, ok := provider.(TokenCapacity)
	if !ok {
		return configuredLimit, nil
	}
	limit, err := capacity.ContextTokenLimit(ctx)
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if err != nil {
		return 0, err
	}
	if limit <= PromptTemplateReserve || limit > ContextTokenLimit {
		return 0, ErrLocalCompletionConfig
	}
	return min(configuredLimit, limit), nil
}

func CompletionContextLimit(ctx context.Context, client LocalCompletion) (int, error) {
	if client == nil {
		return 0, ErrLocalCompletionConfig
	}
	return RuntimeContextLimit(ctx, client, ContextTokenLimit)
}

func CountRequestInputTokens(ctx context.Context, client LocalCompletion, request Request) (int, error) {
	if ctx == nil || client == nil {
		return 0, ErrLocalCompletionConfig
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var count int
	var err error
	if counter, ok := client.(RequestTokenCounter); ok {
		count, err = counter.CountRequestTokens(ctx, request)
	} else {
		count, err = client.CountInputTokens(ctx, request.System, request.User)
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if err != nil {
		return 0, err
	}
	if count < 0 {
		return 0, ErrLocalCompletionConfig
	}
	return count, nil
}

func (o *Ollama) Identity(ctx context.Context) (string, error) {
	identity, err := o.ResolveCompletionModel(ctx)
	return identity.Digest, err
}
