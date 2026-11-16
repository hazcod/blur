package assistant

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/model"
)

type transcriptRepairGenerator struct{ packed []PackedContext }

func (g *transcriptRepairGenerator) Generate(_ context.Context, packed PackedContext, _ int) (ModelAnswer, error) {
	g.packed = append(g.packed, packed)
	if len(g.packed) == 1 {
		return ModelAnswer{Text: "UNVERIFIED_CLAIM", SourceIDs: []string{strings.Repeat("f", 64)}}, nil
	}
	return ModelAnswer{Text: "TCP adapts to congestion.", SourceIDs: packed.SourceIDs[:1]}, nil
}

func TestTranscriptSummaryRepairsOnceWithFreshActualAdmission(t *testing.T) {
	g := &transcriptRepairGenerator{}
	counter := &transcriptTokenCounter{}
	result, err := NewService(nil, g, counter, ContextTokenLimit).SummarizeTranscript(t.Context(), transcriptEvidence(50))
	if err != nil || len(g.packed) != 2 || counter.capacity != 2 || counter.counts < 2 || counter.generic != 0 || result.InputTokens != g.packed[1].InputBytes+200 || result.InputTokens+MaxAnswerTokens+PromptTemplateReserve > 4096 || strings.Contains(result.Answer.Text, "UNVERIFIED_CLAIM") || !strings.Contains(g.packed[1].System, "previous response was rejected") || !strings.Contains(g.packed[1].System, "source_labels") || strings.Contains(g.packed[1].System, "Do not return source labels") || result.SentItems != len(g.packed[1].SourceIDs) {
		t.Fatal("strict repair did not recount the actual changed request or preserved invalid prose", len(g.packed), counter, err)
	}
}

func TestTranscriptSummaryDoesNotRepairRefusalOrCancellation(t *testing.T) {
	for _, expected := range []error{model.ErrAppleRefused, model.ErrAppleUnavailable, context.Canceled, context.DeadlineExceeded} {
		g := &invalidAnswerGenerator{err: expected}
		result, err := NewService(nil, g, testCounter{tokens: 100}, ContextTokenLimit).SummarizeTranscript(t.Context(), transcriptEvidence(1))
		if !errors.Is(err, expected) || result.Answer.Text != "" || g.calls != 1 {
			t.Fatal("provider refusal/cancellation triggered format repair", expected, err, g.calls)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	g := &invalidAnswerGenerator{err: ErrAnswer, after: cancel}
	result, err := NewService(nil, g, testCounter{tokens: 100}, ContextTokenLimit).SummarizeTranscript(ctx, transcriptEvidence(1))
	if !errors.Is(err, context.Canceled) || result.Answer.Text != "" || g.calls != 1 {
		t.Fatal("cancellation allowed a strict repair or stale result", err, g.calls)
	}
}

func transcriptEvidence(n int) []Evidence {
	evidence := make([]Evidence, n)
	for i := range evidence {
		evidence[i] = Evidence{SourceID: fmt.Sprintf("%064x", i+1), AppID: "sh.blur.meetings", AppName: "Meeting transcript (system)", Domain: fmt.Sprintf("fragment-%d.meeting.local", i), Text: fmt.Sprintf("TCP congestion control passage %d explains the sending rate.", i)}
	}
	return evidence
}

func TestTranscriptSummaryCompactSourcesDoNotRaiseNormalAskLimit(t *testing.T) {
	evidence := transcriptEvidence(50)
	ids := make([]string, len(evidence))
	for i := range evidence {
		ids[i] = evidence[i].SourceID
	}
	g := &testGenerator{answer: ModelAnswer{Text: "TCP adapts its sending rate to congestion.", SourceIDs: ids}}
	r := &testRetriever{}
	result, err := NewService(r, g, testCounter{tokens: 100}, ContextTokenLimit).SummarizeTranscript(t.Context(), evidence)
	if err != nil || r.calls != 0 || result.SentItems != 50 || len(result.Sources) != 50 || !g.packed.Transcript || result.CoverageLimited || strings.Count(g.packed.User, "Meeting transcript (system)") != 1 || strings.Contains(g.packed.User, ".meeting.local") {
		t.Fatal("transcript transform lost compact source authority", result.SentItems, len(result.Sources), err)
	}
	if _, err := ValidateAnswer(g.answer, ids); !errors.Is(err, ErrAnswer) {
		t.Fatal("transcript allowance weakened normal answer validation")
	}
	normal, err := PackContext("What did I hear?", evidence)
	if err != nil || len(normal.SourceIDs) != MaxEvidenceItems || normal.Transcript {
		t.Fatal("transcript allowance raised normal Ask packing limit", err)
	}
}

func TestTranscriptSummaryRejectsInsufficientAndInvalidAnswers(t *testing.T) {
	evidence := transcriptEvidence(2)
	for _, scenario := range []struct {
		name   string
		answer ModelAnswer
		want   error
	}{
		{"insufficient", ModelAnswer{InsufficientEvidence: true}, ErrSummaryInsufficientEvidence},
		{"unknown-source", ModelAnswer{Text: "Invented claim.", SourceIDs: []string{strings.Repeat("f", 64)}}, ErrAnswer},
		{"duplicate", ModelAnswer{Text: "Repeated citation.", SourceIDs: []string{evidence[0].SourceID, evidence[0].SourceID}}, ErrAnswer},
		{"empty-source", ModelAnswer{Text: "Uncited claim."}, ErrAnswer},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			g := &testGenerator{answer: scenario.answer}
			result, err := NewService(nil, g, testCounter{tokens: 100}, ContextTokenLimit).SummarizeTranscript(t.Context(), evidence)
			calls := 2 // One bounded strict format repair, still rejected.
			if scenario.answer.InsufficientEvidence {
				calls = 1
			}
			if !errors.Is(err, scenario.want) || result.Answer.Text != "" || len(result.Sources) != 0 || g.calls != calls {
				t.Fatal("invalid transcript answer became a recap", err)
			}
		})
	}
	for _, scenario := range []string{"foreign-app", "duplicate-source", "foreign-domain", "inferred", "excerpt", "too-many"} {
		t.Run(scenario, func(t *testing.T) {
			evidence := transcriptEvidence(2)
			switch scenario {
			case "foreign-app":
				evidence[0].AppID = "browser"
			case "duplicate-source":
				evidence[1].SourceID = evidence[0].SourceID
			case "foreign-domain":
				evidence[0].Domain = "youtube.com"
			case "inferred":
				evidence[0].Origin = OriginInferred
			case "excerpt":
				evidence[0].MatchEnd = 10
			case "too-many":
				evidence = transcriptEvidence(51)
			}
			g := &testGenerator{}
			if _, err := NewService(nil, g, testCounter{tokens: 100}, ContextTokenLimit).SummarizeTranscript(t.Context(), evidence); !errors.Is(err, ErrContext) || g.calls != 0 {
				t.Fatal("non-transcript authority entered compact context", err)
			}
		})
	}
}

type transcriptTokenCounter struct{ counts, generic, capacity int }

func (c *transcriptTokenCounter) ContextTokenLimit(context.Context) (int, error) {
	c.capacity++
	return 4096, nil
}
func (c *transcriptTokenCounter) CountInputTokens(context.Context, string, string) (int, error) {
	c.generic++
	return 100000, nil
}
func (c *transcriptTokenCounter) CountMemoryInputTokens(_ context.Context, system, user string) (int, error) {
	c.counts++
	return len(system) + len(user) + 200, nil
}

func TestTranscriptAdmissionUsesBudgetAwareCounter(t *testing.T) {
	counter := &providerBudgetCounter{limit: 4096, schemaTokens: 200}
	_, _, err := prepareTranscriptInput(context.Background(), counter, ContextTokenLimit, transcriptEvidence(2), false)
	if err != nil || counter.budgetCalls == 0 || counter.lastBudget != counter.limit-MaxAnswerTokens-PromptTemplateReserve {
		t.Fatalf("transcript budget admission calls=%d budget=%d error=%v", counter.budgetCalls, counter.lastBudget, err)
	}
}

func TestTranscriptSummaryCountsActualGuidanceAgainstRuntimeContext(t *testing.T) {
	evidence := transcriptEvidence(50)
	g := &testGenerator{answer: ModelAnswer{Text: "TCP adapts its sending rate.", SourceIDs: []string{evidence[0].SourceID}}}
	counter := &transcriptTokenCounter{}
	result, err := NewService(nil, g, counter, ContextTokenLimit).SummarizeTranscript(t.Context(), evidence)
	if err != nil || counter.capacity != 1 || counter.generic != 0 || counter.counts < 2 || g.calls != 1 || result.InputTokens != g.packed.InputBytes+200 || result.InputTokens+MaxAnswerTokens+PromptTemplateReserve > 4096 || result.SentItems >= 50 || result.Truncated != 0 {
		t.Fatal("transcript ignored runtime capacity or exact request cost", result.InputTokens, result.SentItems, counter, err)
	}
}

func TestTranscriptSummaryAppleSchemaCountMatchesFiftySourceGeneration(t *testing.T) {
	evidence := transcriptEvidence(50)
	ports := &appleSchemaPorts{}
	g := &OllamaGenerator{client: ports, schema: []byte(appleAnswerSchema)}
	result, err := NewService(nil, g, g, ContextTokenLimit).SummarizeTranscript(t.Context(), evidence)
	if err != nil || result.SentItems != 50 || !reflect.DeepEqual(ports.counted.Schema, ports.generated.Schema) || ports.counted.System != ports.generated.System || ports.counted.User != ports.generated.User || ports.counted.Format != ports.generated.Format {
		t.Fatal("transcript count and generation used different source schema or guidance", result.SentItems, err)
	}
	ids := make([]string, 50)
	for i := range ids {
		ids[i] = evidence[i].SourceID
	}
	valid := `{"answer":"TCP adapts its sending rate.","source_labels":["S1"],"insufficient_evidence":false}`
	parsed, err := parseGeneratedAnswerWithLimit(valid, ids, MaxTranscriptItems)
	if err != nil || !reflect.DeepEqual(parsed.SourceIDs, ids[:1]) {
		t.Fatal("transcript evidence label did not resolve", err)
	}
	if _, err := parseGeneratedAnswer(valid, ids); err == nil {
		t.Fatal("normal parser admitted a transcript-sized context")
	}
	for _, labels := range []string{`["S51"]`, `["S1","S1"]`, `["S0"]`, `["S01"]`, `["ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"]`} {
		parsed, err := parseGeneratedAnswerWithLimit(`{"answer":"Grounded transcript summary.","source_labels":`+labels+`,"insufficient_evidence":false}`, ids, MaxTranscriptItems)
		if err == nil || len(parsed.SourceIDs) != 0 {
			t.Fatal("invalid transcript evidence labels were accepted", err)
		}
	}
}
