package assistant

import (
	"log/slog"

	"github.com/hazcod/blur/internal/model"
)

// Only closed codes and counts leave this boundary. Never log a prompt,
// response, question, source handle, or the provider's raw error text.
func logGroundedAnswerFailure(logger *slog.Logger, err error, sourceCount int) {
	if logger == nil {
		return
	}
	reason := "unknown"
	switch err {
	case ErrGeneratorResponse:
		reason = "generated_structure"
	case ErrAnswer:
		reason = "answer_validation"
	case model.ErrAppleResponse:
		reason = "apple_response"
	case model.ErrOllamaResponse:
		reason = "ollama_response"
	case model.ErrCompatibleResponse:
		reason = "compatible_response"
	case model.ErrAnthropicResponse:
		reason = "anthropic_response"
	}
	logger.Warn("assistant grounded answer rejected", "reason", reason, "source_count", sourceCount)
}
