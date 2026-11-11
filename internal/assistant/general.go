package assistant

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/model"
)

func validateGeneralRequest(ctx context.Context, counter TokenCounter, question string, maxOutputTokens int) error {
	if ctx == nil || counter == nil || maxOutputTokens < 1 || maxOutputTokens > generalAnswerTokens {
		return ErrAskUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	question, err := normalizeQuestion(question)
	if err != nil {
		return err
	}
	tokens, err := counter.CountInputTokens(ctx, generalSystemPrompt, question)
	if err != nil {
		return err
	}
	if tokens < 0 || tokens+maxOutputTokens+PromptTemplateReserve > ContextTokenLimit {
		return ErrTokenBudget
	}
	return nil
}

func validateGeneralAnswer(answer string) (string, error) {
	answer = strings.TrimSpace(answer)
	if len(answer) == 0 || len(answer) > MaxAnswerBytes || !utf8.ValidString(answer) || strings.ContainsRune(answer, 0) {
		return "", ErrAnswer
	}
	return answer, nil
}

func (g *OllamaGenerator) GenerateGeneral(ctx context.Context, question string, maxOutputTokens int) (string, error) {
	if g == nil || g.client == nil {
		return "", ErrAskUnavailable
	}
	if err := validateGeneralRequest(ctx, g, question, maxOutputTokens); err != nil {
		return "", err
	}
	response, err := g.client.Generate(ctx, model.Request{System: generalSystemPrompt, User: question, MaxOutputTokens: maxOutputTokens})
	if err != nil {
		return "", err
	}
	return validateGeneralAnswer(response.Text)
}

func (g *OpenAICompatibleGenerator) GenerateGeneral(ctx context.Context, question string, maxOutputTokens int) (string, error) {
	if g == nil || g.client == nil {
		return "", ErrAskUnavailable
	}
	if err := validateGeneralRequest(ctx, g, question, maxOutputTokens); err != nil {
		return "", err
	}
	answer, err := g.client.Generate(ctx, generalSystemPrompt, question, maxOutputTokens)
	if err != nil {
		return "", err
	}
	return validateGeneralAnswer(answer)
}
