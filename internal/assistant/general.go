package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/model"
)

func (g *OllamaGenerator) GenerateGeneralPrompt(ctx context.Context, system, user string, maxOutputTokens int) (string, error) {
	if g == nil || g.client == nil || maxOutputTokens < 1 || maxOutputTokens > generalAnswerTokens {
		return "", ErrAskUnavailable
	}
	response, err := g.client.Generate(ctx, model.Request{System: system, User: user, MaxOutputTokens: maxOutputTokens, KeepAlive: answerModelKeepAlive})
	if err != nil {
		return "", err
	}
	return validateGeneralAnswer(response.Text)
}

func (g *OllamaGenerator) GenerateGeneralJSONPrompt(ctx context.Context, system, user string, schema json.RawMessage, maxOutputTokens int) (string, error) {
	if g == nil || g.client == nil || ctx == nil || len(schema) == 0 || len(schema) > 4096 || maxOutputTokens < 1 || maxOutputTokens > generalAnswerTokens {
		return "", ErrAskUnavailable
	}
	request := model.Request{System: system, User: user, Schema: schema, MaxOutputTokens: maxOutputTokens, KeepAlive: answerModelKeepAlive}
	tokens, err := model.CountRequestInputTokens(ctx, g.client, request)
	if err != nil {
		return "", err
	}
	if _, ok := g.client.(model.RequestTokenCounter); !ok {
		tokens += len(schema)
	}
	limit, err := g.ContextTokenLimit(ctx)
	if err != nil {
		return "", err
	}
	if tokens < 0 || tokens > limit-maxOutputTokens-PromptTemplateReserve {
		return "", ErrTokenBudget
	}
	response, err := g.client.Generate(ctx, request)
	if err != nil {
		return "", err
	}
	return validateGeneralAnswer(response.Text)
}

func (g *OpenAICompatibleGenerator) GenerateGeneralPrompt(ctx context.Context, system, user string, maxOutputTokens int) (string, error) {
	if g == nil || g.client == nil || maxOutputTokens < 1 || maxOutputTokens > generalAnswerTokens {
		return "", ErrAskUnavailable
	}
	response, err := g.client.Generate(ctx, system, user, maxOutputTokens)
	if err != nil {
		return "", err
	}
	return validateGeneralAnswer(response)
}

func packGeneralRules(question string, rules []string) (string, string, error) {
	question, err := normalizeQuestion(question)
	if err != nil {
		return "", "", err
	}
	if _, err = validateWritingRules(rules); err != nil {
		return "", "", err
	}
	if len(rules) == 0 {
		return generalSystemPrompt, question, nil
	}
	data, err := json.Marshal(struct {
		Question string   `json:"question"`
		Rules    []string `json:"writing_rules"`
	}{question, rules})
	if err != nil {
		return "", "", ErrContext
	}
	return generalSystemPrompt + writingRulePrompt, string(data), nil
}

func (g *OllamaGenerator) GenerateGeneralWithRules(ctx context.Context, question string, rules []string, maxOutputTokens int) (string, error) {
	if g == nil || g.client == nil || ctx == nil || maxOutputTokens < 1 || maxOutputTokens > generalAnswerTokens {
		return "", ErrAskUnavailable
	}
	system, user, err := packGeneralRules(question, rules)
	if err != nil {
		return "", err
	}
	response, err := g.client.Generate(ctx, model.Request{System: system, User: user, MaxOutputTokens: maxOutputTokens, KeepAlive: answerModelKeepAlive})
	if err != nil {
		return "", err
	}
	return validateGeneralAnswer(response.Text)
}

func (g *OpenAICompatibleGenerator) GenerateGeneralWithRules(ctx context.Context, question string, rules []string, maxOutputTokens int) (string, error) {
	if g == nil || g.client == nil || ctx == nil || maxOutputTokens < 1 || maxOutputTokens > generalAnswerTokens {
		return "", ErrAskUnavailable
	}
	system, user, err := packGeneralRules(question, rules)
	if err != nil {
		return "", err
	}
	answer, err := g.client.Generate(ctx, system, user, maxOutputTokens)
	if err != nil {
		return "", err
	}
	return validateGeneralAnswer(answer)
}

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
	response, err := g.client.Generate(ctx, model.Request{System: generalSystemPrompt, User: question, MaxOutputTokens: maxOutputTokens, KeepAlive: answerModelKeepAlive})
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
