package assistant

import (
	"context"
	"strings"
	"testing"
)

type documentCountingGenerator struct{ calls int }

func (g *documentCountingGenerator) Generate(_ context.Context, packed PackedContext, _ int) (ModelAnswer, error) {
	g.calls++
	return ModelAnswer{Text: "The page describes a technical system.", SourceIDs: append([]string(nil), packed.SourceIDs...)}, nil
}

func TestWholeDocumentReductionUsesFewerRequestsForLargePages(t *testing.T) {
	const pageBytes = 1 << 20
	text := strings.Repeat("x", pageBytes)
	source := Evidence{
		SourceID: strings.Repeat("1", 64), Layer: LayerSource, Text: text,
		Document: DocumentProvenance{
			EditionID: strings.Repeat("2", 64), FamilyID: strings.Repeat("3", 64), Publication: strings.Repeat("4", 64),
			PartIndex: 0, PartCount: 1, TotalBytes: pageBytes, Start: 0, End: pageBytes,
		},
	}
	generator := &documentCountingGenerator{}
	service := NewService(&documentTestRetriever{parts: []Evidence{source}, candidates: []Evidence{source}, byID: map[string]Evidence{source.SourceID: source}}, generator, documentTestCounter{}, ContextTokenLimit)
	plan := Plan{Kind: KindLookup, WholeDocument: true, Question: "Summarize the whole page"}
	result, err := service.answerWholeDocument(context.Background(), plan.Question, plan, Retrieval{Evidence: []Evidence{source}}, nil, nil)
	if err != nil || result.Answer.InsufficientEvidence {
		t.Fatalf("large-page analysis failed: result=%+v err=%v", result, err)
	}
	t.Logf("1 MiB whole-page analysis used %d model requests", generator.calls)
	if generator.calls > 52 {
		t.Fatalf("1 MiB whole-page analysis used %d model requests; want at most 52", generator.calls)
	}
}
