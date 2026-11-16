package assistant

import (
	"context"
	"strings"
	"testing"
)

type providerBudgetCounter struct {
	limit, schemaTokens                  int
	memoryCalls, plainCalls, budgetCalls int
	lastBudget                           int
}

func (c *providerBudgetCounter) ContextTokenLimit(context.Context) (int, error) { return c.limit, nil }
func (c *providerBudgetCounter) CountInputTokens(_ context.Context, system, user string) (int, error) {
	c.plainCalls++
	return len(system) + len(user), nil
}
func (c *providerBudgetCounter) CountMemoryInputTokens(_ context.Context, system, user string) (int, error) {
	c.memoryCalls++
	return len(system) + len(user) + c.schemaTokens, nil
}
func (c *providerBudgetCounter) CountMemoryInputTokensWithinBudget(ctx context.Context, system, user string, budget int) (int, error) {
	c.budgetCalls++
	c.lastBudget = budget
	return c.CountMemoryInputTokens(ctx, system, user)
}

func TestGroundedPackingIncludesProviderSchemaAndRuntimeCapacity(t *testing.T) {
	id := strings.Repeat("a", 64)
	r := &testRetriever{evidence: []Evidence{{SourceID: id, Text: strings.Repeat("A supplied fact. ", 400)}}}
	g := &testGenerator{answer: ModelAnswer{Text: "A supplied fact.", SourceIDs: []string{id}}}
	counter := &providerBudgetCounter{limit: 4096, schemaTokens: 600}
	result, err := NewService(r, g, counter, 8192).Ask(context.Background(), "What did I do?")
	if err != nil || g.calls != 1 || counter.memoryCalls == 0 || counter.budgetCalls != counter.memoryCalls || counter.lastBudget != counter.limit-MaxAnswerTokens-PromptTemplateReserve || counter.plainCalls != 0 || result.InputTokens+MaxAnswerTokens+PromptTemplateReserve > counter.limit || g.packed.InputBytes+counter.schemaTokens+MaxAnswerTokens+PromptTemplateReserve > counter.limit || result.Truncated == 0 {
		t.Fatalf("runtime/schema admission failed: err=%v generated=%d memory_counts=%d budget_counts=%d budget=%d plain_counts=%d input=%d bytes=%d truncated=%d", err, g.calls, counter.memoryCalls, counter.budgetCalls, counter.lastBudget, counter.plainCalls, result.InputTokens, g.packed.InputBytes, result.Truncated)
	}
}
