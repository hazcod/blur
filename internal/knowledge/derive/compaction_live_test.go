package derive

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/model"
)

type syntheticCompactionTrace struct {
	model.LocalCompletion
	last string
}

func (c *syntheticCompactionTrace) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	r, err := c.LocalCompletion.Generate(ctx, request)
	c.last = r.Text
	return r, err
}
func (c *syntheticCompactionTrace) ContextTokenLimit(ctx context.Context) (int, error) {
	return model.CompletionContextLimit(ctx, c.LocalCompletion)
}
func (c *syntheticCompactionTrace) CountRequestTokens(ctx context.Context, r model.Request) (int, error) {
	return model.CountRequestInputTokens(ctx, c.LocalCompletion, r)
}

func TestAppleSourceCompactionSyntheticLargeSpan(t *testing.T) {
	if os.Getenv("BLUR_SYNTHETIC_APPLE_BACKGROUND") != "1" {
		t.Skip("explicit synthetic on-device check only")
	}
	base, err := NewAppleGenerator()
	if err != nil {
		t.Fatal(err)
	}
	trace := &syntheticCompactionTrace{LocalCompletion: base.client}
	base.client = trace
	g := WithCompaction(base, false)
	defer g.CloseIdleConnections()
	var log strings.Builder
	for round := range 12 {
		for agent := range 10 {
			fmt.Fprintf(&log, "Round %d: agent_build_%02d validated its parser; queue checkpoint restoration awaits Morgan's review. This saved log does not establish the computer user's own actions.\n", round, agent)
		}
	}
	in := []Evidence{{SourceID: sourceID('a'), Text: log.String()[:4096]}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	tokens, err := g.CountCompactedInputTokens(ctx, in, 512)
	if err != nil {
		t.Fatal(err)
	}
	_, summary, err := g.GenerateCompacted(ctx, in, 512)
	if err != nil {
		// This opt-in fixture contains only the synthetic log above.
		t.Logf("Synthetic provider response: %q", trace.last)
		t.Fatal("large native span", tokens, err)
	}
	t.Logf("Synthetic large span: %d input tokens, %d summary bytes", tokens, len(summary))
}

func TestAppleSourceCompactionSynthetic(t *testing.T) {
	if os.Getenv("BLUR_SYNTHETIC_APPLE_BACKGROUND") != "1" {
		t.Skip("explicit synthetic on-device check only")
	}
	base, err := NewAppleGenerator()
	if err != nil {
		t.Fatal(err)
	}
	g := WithCompaction(base, true)
	defer g.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	in := []Evidence{{SourceID: sourceID('a'), Text: "Agent review_large_context completed parser validation. The remaining issue is restoring queue checkpoints. Morgan will review the change. This is a visible terminal work log, not proof that this computer's user performed each action."}}
	if tokens, err := g.CountCompactedInputTokens(ctx, in, 512); err != nil || tokens+512+model.PromptTemplateReserve > model.ContextTokenLimit {
		t.Fatal("native request admission", tokens, err)
	}
	p, s, err := g.GenerateCompacted(ctx, in, 512)
	if err != nil {
		t.Fatal("native source compaction", err)
	}
	if !strings.Contains(s, "review_large_context") || !strings.Contains(strings.ToLower(s), "checkpoint") {
		t.Fatal("technical subject or unresolved work lost", s)
	}
	if ValidateSuggestions(in, p) != nil || s == "" {
		t.Fatal("invalid native compaction", p, s)
	}
	t.Logf("Native compaction accepted: %d summary bytes, %d proposals", len(s), len(p))
}
