package enrich

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/power"
)

// A work bound changes request size, never the source's eventual coverage.
func (s *Service) admitCompaction(ctx context.Context, g compactingGenerator, policy power.Effective, text string, chunk Chunk) (Chunk, int, int, error) {
	capacity, err := model.RuntimeContextLimit(ctx, s.generator, model.ContextTokenLimit)
	if err != nil {
		return chunk, 0, 0, err
	}
	// Reserve a quarter of the actual request allowance for output. This is
	// per-call admission, not a maximum retained summary size for the source.
	outputTokens := min(min(capacity-model.PromptTemplateReserve, policy.MaxBackgroundTokens)/4, model.MaxCompletionTokens)
	if outputTokens < 1 {
		return chunk, 0, 0, ErrPolicy
	}
	for size := derive.MaxEvidenceItemBytes; size >= utf8.UTFMax; size /= 2 {
		chunk.SpanBytes = size
		chunk.End, chunk.Next, chunk.Complete, err = NextSizedChunk(text, chunk.Start, size)
		if err != nil {
			return chunk, 0, outputTokens, err
		}
		span := strings.TrimSpace(text[chunk.Start:chunk.End])
		if span == "" {
			return chunk, 0, outputTokens, nil
		}
		tokens, err := g.CountCompactedInputTokens(ctx, []derive.Evidence{{SourceID: chunk.SourceID, Text: span}}, outputTokens)
		if err != nil {
			return chunk, 0, outputTokens, err
		}
		fits := tokens > 0 && tokens+outputTokens+model.PromptTemplateReserve <= min(capacity, model.ContextTokenLimit)
		allowed := policy.AllowBackgroundAI(power.LocalProvider, tokens+outputTokens, min(policy.MaxBackgroundDuration, time.Minute))
		if fits && allowed {
			return chunk, tokens, outputTokens, nil
		}
		if size == utf8.UTFMax {
			if !fits {
				return chunk, 0, outputTokens, ErrCapacity
			}
			return chunk, 0, outputTokens, ErrPolicy
		}
	}
	return chunk, 0, outputTokens, ErrCapacity
}
