package notes

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/model"
)

// AppleExtractor sends only a validated user-authored note to the on-device
// Foundation Models runtime. Its output remains an unconfirmed proposal.
type AppleExtractor struct{ client model.LocalCompletion }

const appleProposalSchema = `{"type":"object","properties":{"proposals":{"type":"array","maxItems":8,"items":{"type":"object","properties":{"kind":{"type":"string","enum":["task","reminder","follow_up","question"]},"text":{"type":"string"},"due":{"type":"string"}},"required":["kind","text"],"additionalProperties":false}}},"required":["proposals"],"additionalProperties":false}`

func NewAppleExtractor() (*AppleExtractor, error) {
	client, err := model.NewAppleSourceTransformer()
	if err != nil {
		return nil, err
	}
	return &AppleExtractor{client: client}, nil
}

func (e *AppleExtractor) CloseIdleConnections() {
	if e != nil && e.client != nil {
		e.client.CloseIdleConnections()
	}
}

func (e *AppleExtractor) Extract(ctx context.Context, observation capture.SanitizedObservation) ([]Proposal, error) {
	if e == nil || e.client == nil {
		return nil, ErrInvalid
	}
	if err := validateNoteExtractionSource(ctx, observation); err != nil {
		return nil, err
	}
	request := model.Request{
		System: noteExtractionInstructions, User: "Note:\n" + observation.Text(),
		Schema: json.RawMessage(appleProposalSchema), MaxOutputTokens: maxOllamaOutputTokens,
	}
	// Keep the complete saved note separate from the bounded model request. A
	// byte-limit failure is explicit; never silently send only its prefix.
	if len(request.System)+len(request.User) > model.MaxRequestBytes {
		return nil, ErrModelBudget
	}
	tokens, err := model.CountRequestInputTokens(ctx, e.client, request)
	if err != nil {
		return nil, appleExtractionError(err)
	}
	schemaReserve := 0
	if _, exact := e.client.(model.RequestTokenCounter); !exact {
		schemaReserve = len(request.Schema)
	}
	capacity, err := model.CompletionContextLimit(ctx, e.client)
	if err != nil {
		return nil, appleExtractionError(err)
	}
	if tokens > capacity-request.MaxOutputTokens-model.PromptTemplateReserve-schemaReserve {
		return nil, ErrModelBudget
	}
	response, err := e.client.Generate(ctx, request)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, appleExtractionError(err)
	}
	return decodeModelProposals(response.Text)
}

func appleExtractionError(err error) error {
	if errors.Is(err, model.ErrAppleBudget) {
		return ErrModelBudget
	}
	return err
}

var _ Extractor = (*AppleExtractor)(nil)
