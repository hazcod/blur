package derive

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/model"
)

func TestCompactionUsesExactIndependentSourceSpan(t *testing.T) {
	c := &appleCompletionStub{count: 800, text: `{"summary":"Atlas: parser finished; deployment blocked.","suggestions":[]}`}
	g := WithCompaction(newAppleGenerator(c), true)
	in := []Evidence{{SourceID: sourceID('a'), Text: "Deployment is blocked."}}
	if n, err := g.CountCompactedInputTokens(context.Background(), in, 512); err != nil || n != 800 {
		t.Fatal(n, err)
	}
	counted := c.request
	p, summary, err := g.GenerateCompacted(context.Background(), in, 512)
	if err != nil || len(p) != 0 || summary != "Atlas: parser finished; deployment blocked." {
		t.Fatal(p, summary, err)
	}
	if c.request.User != counted.User || c.request.System != counted.System || strings.Contains(c.request.User, "previous_summary") || !strings.Contains(c.request.User, in[0].Text) || strings.Contains(c.request.User, in[0].SourceID) || c.request.MaxOutputTokens != 512 {
		t.Fatal("count/generation differ or exact source span missing", c.request)
	}
	a, _ := g.Identity(context.Background())
	b, _ := newAppleGenerator(c).Identity(context.Background())
	if a == b {
		t.Fatal("old completed receipts would skip compaction")
	}
}

func TestCompactionKeepsTechnicalSubjectsWhenModelOmitsTheirNames(t *testing.T) {
	c := &appleCompletionStub{text: `{"summary":"Parser validation completed; checkpoints still pending.","suggestions":[]}`}
	g := WithCompaction(newAppleGenerator(c), false)
	_, s, err := g.GenerateCompacted(context.Background(), []Evidence{{SourceID: sourceID('a'), Text: "Agent review_large_context finished src/parser.go. Agent review_queue_work awaits checkpoints."}}, 512)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"review_large_context", "src/parser.go", "review_queue_work"} {
		if !strings.Contains(s, name) {
			t.Fatal("technical subject anonymized", name, s)
		}
	}
}

func TestSummaryOnlyCompactionAcceptsOmittedDisabledSuggestions(t *testing.T) {
	c := &appleCompletionStub{text: `{"summary":"Parsers validated; checkpoint restoration awaits review."}`}
	in := []Evidence{{SourceID: sourceID('a'), Text: "Agent parser validated; checkpoint restoration awaits review."}}
	g := WithCompaction(newAppleGenerator(c), false)
	p, summary, err := g.GenerateCompacted(context.Background(), in, 512)
	if err != nil || len(p) != 0 || !strings.Contains(summary, "checkpoint") {
		t.Fatal("disabled empty claims field stranded a valid source summary", p, summary, err)
	}
	g = WithCompaction(newAppleGenerator(c), true)
	if _, _, err := g.GenerateCompacted(context.Background(), in, 512); !errors.Is(err, model.ErrOllamaResponse) {
		t.Fatal("enabled extraction accepted missing extraction result", err)
	}
}

func TestCompactionAcceptsEscapedWireTextWithinProviderEnvelope(t *testing.T) {
	wire, _ := json.Marshal(map[string]any{"summary": strings.Repeat("<", 1400), "suggestions": []any{}})
	c := &appleCompletionStub{text: string(wire)}
	g := WithCompaction(newAppleGenerator(c), false)
	_, summary, err := g.GenerateCompacted(context.Background(), []Evidence{{SourceID: sourceID('a'), Text: "A technical source."}}, 512)
	if err != nil || summary != strings.Repeat("<", 1400) {
		t.Fatal("wire escaping was mistaken for excess source content", len(wire), err)
	}
}

func TestCompactionRejectsInvalidOrForeignOutput(t *testing.T) {
	c := &appleCompletionStub{}
	g := WithCompaction(newAppleGenerator(c), true)
	in := []Evidence{{SourceID: sourceID('a'), Text: "Atlas parser finished."}}
	for _, text := range []string{
		`{"summary":"","suggestions":[]}`,
		`{"summary":"ok","suggestions":[],"state":"complete"}`,
		`{"summary":"ok","suggestions":[]} {}`,
		`{"summary":"ok","suggestions":[{"kind":"project","name":"Atlas","fact":"Parser finished.","source_id":"e2"}]}`,
		`{"summary":"` + strings.Repeat("x", model.MaxGeneratedContentBytes+1) + `","suggestions":[]}`,
	} {
		c.text = text
		if _, _, err := g.GenerateCompacted(context.Background(), in, 512); !errors.Is(err, model.ErrOllamaResponse) {
			t.Fatal("invalid compaction accepted", err)
		}
	}
	g = WithCompaction(newAppleGenerator(c), false)
	c.text = `{"summary":"Atlas parser finished.","suggestions":[{"kind":"project","name":"Atlas","fact":"Parser finished.","source_id":"e1"}]}`
	if _, _, err := g.GenerateCompacted(context.Background(), in, 512); err == nil {
		t.Fatal("summary-only mode accepted Library claims")
	}
}

func TestCompactionRetainsLargerSpanSummariesAndAppleProtocolRepairs(t *testing.T) {
	summary := strings.Repeat("technical detail; ", 60)
	c := &appleCompletionStub{text: `{summary:"` + summary[:len(summary)-1] + `",suggestions:[{kind:"project",name:"Atlas",fact:"Parser finished.",open_work:false,source_id:e1}]}`}
	g := WithCompaction(newAppleGenerator(c), true)
	p, s, err := g.GenerateCompacted(context.Background(), []Evidence{{SourceID: sourceID('a'), Text: "Atlas parser finished."}}, 512)
	if err != nil || len(s) <= 512 || len(p) != 1 || p[0].SourceID != sourceID('a') {
		t.Fatal("source summary constrained or protocol repair lost", len(s), p, err)
	}
}
