package assistant

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

type documentTestCounter struct{}

func (documentTestCounter) CountInputTokens(_ context.Context, system, user string) (int, error) {
	return (len(system) + len(user)) / 4, nil
}

type documentTestGenerator struct {
	calls   int
	seen    []string
	systems []string
	labels  [][]string
	invalid bool
}

func (g *documentTestGenerator) Generate(_ context.Context, packed PackedContext, _ int) (ModelAnswer, error) {
	g.calls++
	var payload struct {
		Evidence []struct {
			Label string `json:"label"`
			Text  string `json:"text"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(packed.User), &payload); err != nil {
		return ModelAnswer{}, err
	}
	g.systems = append(g.systems, packed.System)
	labels := make([]string, len(payload.Evidence))
	for _, evidence := range payload.Evidence {
		g.seen = append(g.seen, evidence.Text)
	}
	for i, evidence := range payload.Evidence {
		labels[i] = evidence.Label
	}
	g.labels = append(g.labels, labels)
	if g.invalid {
		return ModelAnswer{Text: "unverified claim", SourceIDs: []string{strings.Repeat("f", 64)}}, nil
	}
	if strings.Contains(packed.System, "FINAL WHOLE-DOCUMENT ANSWER") {
		return ModelAnswer{Text: "The rollback owner is Mira.", SourceIDs: append([]string(nil), packed.SourceIDs...)}, nil
	}
	for _, row := range payload.Evidence {
		if strings.Contains(row.Text, "TAIL_ONLY") || strings.Contains(row.Text, "rollback owner is Mira") {
			return ModelAnswer{Text: "A source passage states that the rollback owner is Mira.", SourceIDs: append([]string(nil), packed.SourceIDs...)}, nil
		}
	}
	return ModelAnswer{Text: "The document discusses architecture, design, and migration.", SourceIDs: append([]string(nil), packed.SourceIDs...)}, nil
}

type documentTestRetriever struct {
	parts      []Evidence
	candidates []Evidence
	byID       map[string]Evidence
}

func (r *documentTestRetriever) Retrieve(context.Context, string) ([]Evidence, error) {
	return append([]Evidence(nil), r.candidates...), nil
}
func (r *documentTestRetriever) KnownApps(context.Context) ([]KnownApp, error) { return nil, nil }
func (r *documentTestRetriever) AskTime() time.Time {
	return time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
}
func (r *documentTestRetriever) RetrievePlan(context.Context, Plan) (Retrieval, error) {
	return Retrieval{Evidence: append([]Evidence(nil), r.candidates...)}, nil
}
func (r *documentTestRetriever) StreamWholeDocumentParts(ctx context.Context, source Evidence, yield func(Evidence) error) error {
	for _, part := range r.parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := yield(part); err != nil {
			return err
		}
	}
	return nil
}
func (r *documentTestRetriever) RetrieveSources(_ context.Context, _ Plan, ids []string) (Retrieval, error) {
	var sources []Evidence
	for _, id := range ids {
		if item, ok := r.byID[id]; ok {
			sources = append(sources, item)
		}
	}
	return Retrieval{Evidence: sources}, nil
}

func TestWholeDocumentAskReadsEveryPartAndReturnsOriginalPartCitations(t *testing.T) {
	ctx := context.Background()
	family, edition, publication := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	intro := strings.Repeat("Architecture and migration notes. ", 12000)
	tail := "TAIL_ONLY: rollback owner is Mira."
	parts := []Evidence{
		{SourceID: strings.Repeat("1", 64), AppID: "com.example.Browser", Domain: "docs.example.test", Text: intro, Document: DocumentProvenance{EditionID: edition, FamilyID: family, Publication: publication, PartIndex: 0, PartCount: 2, TotalBytes: int64(len(intro) + len(tail)), Start: 0, End: int64(len(intro))}},
		{SourceID: strings.Repeat("2", 64), AppID: "com.example.Browser", Domain: "docs.example.test", Text: tail, Document: DocumentProvenance{EditionID: edition, FamilyID: family, Publication: publication, PartIndex: 1, PartCount: 2, TotalBytes: int64(len(intro) + len(tail)), Start: int64(len(intro)), End: int64(len(intro) + len(tail))}},
	}
	candidate := parts[0]
	byID := map[string]Evidence{parts[0].SourceID: parts[0], parts[1].SourceID: parts[1]}
	retriever := &documentTestRetriever{parts: parts, candidates: []Evidence{candidate}, byID: byID}
	generator := &documentTestGenerator{}
	service := NewService(retriever, generator, documentTestCounter{}, 2048)
	result, err := service.Ask(ctx, "Summarize the page about architecture")
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer.Text != "The rollback owner is Mira." || result.Plan.WholeDocument == false || len(result.Sources) != 2 {
		t.Fatalf("whole-document answer lost its tail or cited parts: result=%+v", result)
	}
	joined := strings.Join(generator.seen, "\n")
	if !strings.Contains(joined, "Architecture and migration notes.") || !strings.Contains(joined, "TAIL_ONLY: rollback owner is Mira.") {
		t.Fatal("selected model did not see every document segment, including the tail")
	}
	for _, id := range result.Answer.SourceIDs {
		if id != parts[0].SourceID && id != parts[1].SourceID {
			t.Fatalf("answer citation did not resolve to an original part: %q", id)
		}
	}
	if result.SentItems == 0 || result.SentBytes == 0 {
		t.Fatalf("document analysis omitted request accounting: %+v", result)
	}
	for i, system := range generator.systems {
		if !strings.Contains(system, "source_labels") {
			t.Fatalf("document stage %d does not request evidence labels", i)
		}
		for j, label := range generator.labels[i] {
			if label != "S"+strconv.Itoa(j+1) {
				t.Fatalf("document stage %d label %d = %q", i, j, label)
			}
		}
	}
}

func TestWholeDocumentAskClarifiesMultipleFamiliesWithoutCallingModel(t *testing.T) {
	familyA, familyB := strings.Repeat("a", 64), strings.Repeat("d", 64)
	candidates := []Evidence{
		{SourceID: strings.Repeat("1", 64), Text: "First page", Document: DocumentProvenance{EditionID: strings.Repeat("b", 64), FamilyID: familyA, Publication: strings.Repeat("c", 64), PartCount: 1, TotalBytes: 10}},
		{SourceID: strings.Repeat("2", 64), Text: "Second page", Document: DocumentProvenance{EditionID: strings.Repeat("e", 64), FamilyID: familyB, Publication: strings.Repeat("f", 64), PartCount: 1, TotalBytes: 11}},
	}
	generator := &documentTestGenerator{}
	service := NewService(&documentTestRetriever{candidates: candidates}, generator, documentTestCounter{}, 2048)
	result, err := service.Ask(context.Background(), "Summarize the whole page")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Answer.InsufficientEvidence || !strings.Contains(strings.ToLower(result.Answer.Text), "which") || generator.calls != 0 {
		t.Fatalf("ambiguous whole-document request was not clarified: result=%+v model calls=%d", result, generator.calls)
	}
}

func TestWholeDocumentAskRejectsUnverifiedModelCitation(t *testing.T) {
	part := Evidence{SourceID: strings.Repeat("1", 64), AppID: "com.example.Browser", Domain: "docs.example.test", Text: "The rollback owner is Mira."}
	generator := &documentTestGenerator{invalid: true}
	service := NewService(&documentTestRetriever{parts: []Evidence{part}, candidates: []Evidence{part}, byID: map[string]Evidence{part.SourceID: part}}, generator, documentTestCounter{}, 2048)
	result, err := service.Ask(context.Background(), "Summarize the whole page")
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer.Text == "unverified claim" || len(result.Sources) != 0 || result.GenerationIssue != GenerationInvalidResponse {
		t.Fatalf("unverified whole-document answer escaped validation: %+v", result)
	}
}

func TestWholeDocumentAskWithoutModelReturnsLabelledExcerptOfBothEnds(t *testing.T) {
	family, edition, publication := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	intro := "OPENING_MARK. " + strings.Repeat("Architecture and migration notes. ", 12000)
	tail := " Closing thought: CLOSING_MARK."
	prov := func(i, count int64, start, end int) DocumentProvenance {
		return DocumentProvenance{EditionID: edition, FamilyID: family, Publication: publication, PartIndex: i, PartCount: count, TotalBytes: int64(len(intro) + len(tail)), Start: int64(start), End: int64(end)}
	}
	parts := []Evidence{
		{SourceID: strings.Repeat("1", 64), AppID: "com.example.Browser", Domain: "docs.example.test", Text: intro, Document: prov(0, 2, 0, len(intro))},
		{SourceID: strings.Repeat("2", 64), AppID: "com.example.Browser", Domain: "docs.example.test", Text: tail, Document: prov(1, 2, len(intro), len(intro)+len(tail))},
	}
	retriever := &documentTestRetriever{parts: parts, candidates: []Evidence{parts[0]}, byID: map[string]Evidence{parts[0].SourceID: parts[0], parts[1].SourceID: parts[1]}}
	service := NewService(retriever, nil, nil, 2048)
	result, err := service.Ask(context.Background(), "Summarize the page about architecture")
	if err != nil {
		t.Fatal(err)
	}
	text := result.Answer.Text
	if result.Answer.InsufficientEvidence || !result.CoverageLimited || len(result.Sources) != 1 || result.Sources[0].SourceID != parts[0].SourceID {
		t.Fatalf("unexpected result flags or citation: %+v", result)
	}
	if !strings.Contains(text, "OPENING_MARK") || !strings.Contains(text, "CLOSING_MARK") || !strings.Contains(text, "No AI model is configured") {
		t.Fatalf("excerpt lost an end or its model-less label: %q", text)
	}
	if len(text) > 1200+400+400 {
		t.Fatalf("excerpt not bounded: %d bytes", len(text))
	}
}
