package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/model"
)

var ErrGeneratorResponse = errors.New("assistant: invalid generated answer")

const appleTokenEstimateMargin = 128

// Interactive answers keep the already-admitted local model resident briefly
// between questions. No warm-up inference or background source processing is
// requested, and the provider can still unload it under memory pressure.
const answerModelKeepAlive = 10 * time.Minute

// OllamaGenerator is the shared local completion adapter. Its historical name
// also covers Apple Foundation Models while preserving the same validation.
type OllamaGenerator struct {
	client model.LocalCompletion
	schema json.RawMessage
}

// The app attaches citations from admitted evidence; models return answer text only.
const appleAnswerSchema = `{"type":"object","description":"A grounded answer using only the supplied saved evidence.","properties":{"answer":{"type":"string","description":"Answer only from the supplied evidence. If evidence is insufficient, do not invent an answer."},"source_labels":{"type":"array","description":"Labels of the evidence items that directly support the answer. Return an empty array only when evidence is insufficient.","items":{"type":"string"},"maxItems":50},"insufficient_evidence":{"type":"boolean","description":"True only when the supplied evidence cannot support an answer."}},"required":["answer","source_labels","insufficient_evidence"],"additionalProperties":false}`

func NewOllamaGenerator(endpoint, modelName string) (*OllamaGenerator, error) {
	client, err := model.NewOllama(endpoint, modelName)
	if err != nil {
		return nil, err
	}
	return &OllamaGenerator{client: client}, nil
}

func NewAppleGenerator() (*OllamaGenerator, error) {
	client, err := model.NewApple()
	if err != nil {
		return nil, err
	}
	return &OllamaGenerator{client: client, schema: json.RawMessage(appleAnswerSchema)}, nil
}

func (g *OllamaGenerator) transcriptModel() (Generator, TokenCounter, bool) {
	if g == nil {
		return nil, nil, false
	}
	if apple, ok := g.client.(*model.Apple); ok {
		transform := &OllamaGenerator{client: apple.SourceTransformer(), schema: g.schema}
		return transform, transform, true
	}
	return nil, nil, false
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

// CountMemoryInputTokens includes JSON guidance and the grounded answer schema.
// General conversation retains the plain CountInputTokens path.
func (g *OllamaGenerator) CountMemoryInputTokens(ctx context.Context, system, user string) (int, error) {
	if ctx == nil {
		return 0, ErrContext
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if g == nil || g.client == nil {
		return 0, ErrAskUnavailable
	}
	request, err := g.memoryRequest(system, user)
	if err != nil {
		return 0, err
	}
	return model.CountRequestInputTokens(ctx, g.client, request)
}

// CountMemoryInputTokensWithinBudget skips a native tokenizer process for
// short Apple prompts when a conservative byte bound plus margin fits. Large
// or near-limit inputs keep Apple's exact token count before packing.
func (g *OllamaGenerator) CountMemoryInputTokensWithinBudget(ctx context.Context, system, user string, budget int) (int, error) {
	if ctx == nil || budget < 0 {
		return 0, ErrContext
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if g == nil || g.client == nil {
		return 0, ErrAskUnavailable
	}
	request, err := g.memoryRequest(system, user)
	if err != nil {
		return 0, err
	}
	if estimator, ok := g.client.(interface {
		EstimateRequestTokens(model.Request) (int, bool)
	}); ok {
		if estimate, supported := estimator.EstimateRequestTokens(request); supported && estimate >= 0 && estimate <= budget-appleTokenEstimateMargin {
			return estimate + appleTokenEstimateMargin, nil
		}
	}
	return model.CountRequestInputTokens(ctx, g.client, request)
}

func (g *OllamaGenerator) ContextTokenLimit(ctx context.Context) (int, error) {
	if g == nil || g.client == nil {
		return 0, ErrAskUnavailable
	}
	return model.CompletionContextLimit(ctx, g.client)
}

func (g *OllamaGenerator) Generate(ctx context.Context, packed PackedContext, maxOutputTokens int) (ModelAnswer, error) {
	if g == nil || g.client == nil || ctx == nil || ctx.Err() != nil || maxOutputTokens < 1 || maxOutputTokens > MaxAnswerTokens || packed.System == "" || packed.User == "" || len(packed.SourceIDs) == 0 || len(packed.SourceIDs) > packedEvidenceLimit(packed) || packed.InputBytes != len(packed.System)+len(packed.User) || packed.InputBytes+maxOutputTokens+PromptTemplateReserve > ContextTokenLimit || !validPackedSources(packed.SourceIDs) {
		if ctx != nil && ctx.Err() != nil {
			return ModelAnswer{}, ctx.Err()
		}
		return ModelAnswer{}, ErrContext
	}
	request, err := g.memoryRequest(packed.System, packed.User)
	if err != nil {
		return ModelAnswer{}, err
	}
	request.MaxOutputTokens = maxOutputTokens
	request.KeepAlive = answerModelKeepAlive
	response, err := g.client.Generate(ctx, request)
	if err != nil {
		return ModelAnswer{}, err
	}
	return parseGeneratedAnswerWithLimit(response.Text, packed.SourceIDs, packedEvidenceLimit(packed))
}

func parseGeneratedAnswer(content string, sourceIDs []string) (ModelAnswer, error) {
	return parseGeneratedAnswerWithLimit(content, sourceIDs, MaxEvidenceItems)
}

func parseGeneratedAnswerWithLimit(content string, sourceIDs []string, itemLimit int) (ModelAnswer, error) {
	var generated generatedAnswer
	decoder := json.NewDecoder(bytes.NewBufferString(content))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&generated) != nil {
		return ModelAnswer{}, ErrGeneratorResponse
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || strings.TrimSpace(generated.Text) == "" && !generated.InsufficientEvidence {
		return ModelAnswer{}, ErrGeneratorResponse
	}
	if generated.SourceLabels != nil && generated.SourceIDs != nil {
		return ModelAnswer{}, ErrGeneratorResponse
	}
	labels := generated.SourceLabels
	if labels == nil {
		labels = generated.SourceIDs
	}
	selected, err := resolveModelSourceLabelsWithLimit(labels, sourceIDs, itemLimit)
	if err != nil {
		return ModelAnswer{}, err
	}
	answer := ModelAnswer{Text: generated.Text, SourceIDs: selected, InsufficientEvidence: generated.InsufficientEvidence}
	if answer.InsufficientEvidence {
		// Some providers cite inspected context while abstaining. Keep the
		// abstention, not their explanatory prose or any citation. Labels have
		// already been checked against this exact request's admitted sources.
		answer.Text, answer.SourceIDs = InsufficientEvidenceText, nil
	}
	validated, err := validateAnswerWithLimit(answer, sourceIDs, itemLimit)
	if err != nil {
		return ModelAnswer{}, err
	}
	return ModelAnswer(validated), nil
}

func resolveModelSourceLabels(labels, allowedSourceIDs []string) ([]string, error) {
	return resolveModelSourceLabelsWithLimit(labels, allowedSourceIDs, MaxEvidenceItems)
}

func resolveModelSourceLabelsWithLimit(labels, allowedSourceIDs []string, itemLimit int) ([]string, error) {
	if itemLimit < 1 || itemLimit > MaxTranscriptItems || len(labels) > itemLimit || len(allowedSourceIDs) > itemLimit {
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
		if len(label) < 2 || len(label) > 3 || label[0] != 'S' {
			return nil, ErrAnswer
		}
		index, err := strconv.Atoi(label[1:])
		if err != nil || index < 1 || index > len(allowedSourceIDs) || label != "S"+strconv.Itoa(index) {
			return nil, ErrAnswer
		}
		resolved = append(resolved, allowedSourceIDs[index-1])
	}
	return resolved, nil
}

func packedEvidenceLimit(packed PackedContext) int {
	if packed.Transcript {
		return MaxTranscriptItems
	}
	return MaxEvidenceItems
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
