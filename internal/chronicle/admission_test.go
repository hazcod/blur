package chronicle

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/power"
)

// This synthetic tokenizer makes dense Unicode and per-source metadata
// expensive. It exercises admission without contacting a real provider.
type admissionGenerator struct {
	counts         int
	generated      []Packed
	fixedOverhead  int
	perSource      int
	constantTokens int
	maxAdmitted    int
}

func (g *admissionGenerator) CountInputTokens(_ context.Context, p Packed) (int, error) {
	g.counts++
	if g.constantTokens > 0 {
		return g.constantTokens, nil
	}
	tokens := g.fixedOverhead + len(p.Evidence)*g.perSource
	for _, e := range p.Evidence {
		tokens += utf8.RuneCountInString(e.Text())
	}
	return tokens, nil
}
func (*admissionGenerator) ContextTokenLimit(context.Context) (int, error) { return 4096, nil }
func (g *admissionGenerator) Generate(_ context.Context, p Packed, _ int) (Summary, error) {
	tokens := g.fixedOverhead + len(p.Evidence)*g.perSource
	for _, e := range p.Evidence {
		tokens += utf8.RuneCountInString(e.Text())
	}
	if tokens > g.maxAdmitted {
		return Summary{}, errors.New("generation happened before token admission")
	}
	g.generated = append(g.generated, p)
	return Summary{HourID: p.HourID, Start: p.Start, End: p.End, Text: "Reviewed saved notes.", SourceIDs: p.SourceIDs}, nil
}

func TestWorkerShrinksDenseUnicodeExcerptsToBalancedAdmissionBeforeGeneration(t *testing.T) {
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	text := "head material\npassword: x\n" + strings.Repeat("日本語🙂", 900)
	source, err := capture.SanitizeObservation("editor.app", "example.com", text, len(text))
	if err != nil || !source.Truncated() {
		t.Fatalf("partial large fixture: %v", err)
	}
	hour := Hour{Start: start, End: start.Add(time.Hour), Evidence: []Evidence{{Observation: source, At: start}}}
	g := &admissionGenerator{fixedOverhead: 800, maxAdmitted: 1664}
	repo := &workerRepo{rows: map[string]Summary{}}
	w := NewWorker(repo, &workerPolicy{value: balanced()}, g, WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer w.Close()
	if err := w.Process(context.Background(), hour); err != nil {
		t.Fatalf("saved Unicode source was rejected instead of repacked: %v", err)
	}
	if g.counts < 2 || g.counts > 12 || len(g.generated) != 1 {
		t.Fatalf("unbounded or missing admission: counts=%d generated=%d", g.counts, len(g.generated))
	}
	input := g.generated[0]
	if len(input.SourceIDs) != 1 || input.SourceIDs[0] != source.ContentID() || !input.Evidence[0].CapturedPartial() || !input.Evidence[0].Excerpted() || !utf8.ValidString(input.Evidence[0].Text()) {
		t.Fatal("repacking changed identity or lost coverage")
	}
	x := input.Evidence[0].excerpt
	stored := source.Text()
	if x == nil || input.Evidence[0].Text() != stored[:x.headEnd]+excerptGap+stored[x.tailStart:] {
		t.Fatal("recounted excerpt offsets no longer describe the saved original")
	}
	row, found, err := repo.GetChronicle(context.Background(), input.HourID)
	if err != nil || !found || !strings.Contains(row.Text, "1 partial capture") || !strings.Contains(row.Text, "1 source(s) excerpted") {
		t.Fatalf("admitted recap lost coverage: %+v %t %v", row, found, err)
	}
}

func TestWorkerOmitsLowerSalienceSourcesWhenMetadataCannotFitTogether(t *testing.T) {
	hour := workerHour(t)
	hour.Evidence = nil
	for i := 0; i < 5; i++ {
		item := evidence(i+1, hour.Start.Add(time.Duration(i)*time.Minute), "notes")
		item.Salience = uint8(i)
		hour.Evidence = append(hour.Evidence, item)
	}
	wantID := hour.Evidence[4].Observation.ContentID()
	g := &admissionGenerator{fixedOverhead: 900, perSource: 500, maxAdmitted: 1664}
	repo := &workerRepo{rows: map[string]Summary{}}
	w := NewWorker(repo, &workerPolicy{value: balanced()}, g, WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer w.Close()
	if err := w.Process(context.Background(), hour); err != nil {
		t.Fatalf("bounded source omission was not attempted: %v", err)
	}
	if len(g.generated) != 1 || len(g.generated[0].SourceIDs) != 1 || g.generated[0].SourceIDs[0] != wantID || g.generated[0].Omitted != 4 {
		t.Fatalf("repack lost salience or omission coverage: %+v", g.generated)
	}
	row, _, _ := repo.GetChronicle(context.Background(), g.generated[0].HourID)
	if !strings.Contains(row.Text, "4 source(s) omitted") {
		t.Fatal("admission omissions disappeared from stored recap")
	}
}

func TestWorkerRejectsUnavoidableRequestOverheadAfterBoundedRecounts(t *testing.T) {
	hour := workerHour(t)
	hour.Evidence[0] = evidence(1, hour.Start, strings.Repeat("large source ", 1000))
	g := &admissionGenerator{constantTokens: 5000, maxAdmitted: 1664}
	w := NewWorker(&workerRepo{rows: map[string]Summary{}}, &workerPolicy{value: balanced()}, g, WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer w.Close()
	if err := w.Process(context.Background(), hour); !errors.Is(err, ErrWorkerPolicy) {
		t.Fatalf("unavoidable overhead error=%v", err)
	}
	if g.counts < 2 || g.counts > 12 || len(g.generated) != 0 {
		t.Fatalf("unbounded recount or inference without admission: %d/%d", g.counts, len(g.generated))
	}
}

// The real Apple adapter validates request format before observing cancellation.
// A canceled private context therefore exercises its exact byte/schema boundary
// without starting a helper or performing actual model inference.
type appleBoundaryCompletion struct {
	counts    []model.Request
	generated []model.Request
}

func (c *appleBoundaryCompletion) CountRequestTokens(_ context.Context, request model.Request) (int, error) {
	c.counts = append(c.counts, request)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&model.Apple{}).CountRequestTokens(ctx, request)
	if !errors.Is(err, context.Canceled) {
		return 0, err
	}
	return 10, nil
}
func (c *appleBoundaryCompletion) CountInputTokens(ctx context.Context, system, user string) (int, error) {
	return c.CountRequestTokens(ctx, model.Request{System: system, User: user})
}
func (*appleBoundaryCompletion) ContextTokenLimit(context.Context) (int, error) { return 4096, nil }
func (*appleBoundaryCompletion) CloseIdleConnections()                          {}
func (*appleBoundaryCompletion) Identity(context.Context) (string, error) {
	return "synthetic-apple-boundary", nil
}
func (c *appleBoundaryCompletion) Generate(_ context.Context, request model.Request) (model.Response, error) {
	c.generated = append(c.generated, request)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&model.Apple{}).Generate(ctx, request)
	if !errors.Is(err, context.Canceled) {
		return model.Response{}, err
	}
	var payload summaryPayload
	if err := json.Unmarshal([]byte(request.User), &payload); err != nil {
		return model.Response{}, err
	}
	ids := make([]string, 0, len(payload.Evidence))
	for _, item := range payload.Evidence {
		ids = append(ids, item.SourceID)
	}
	sort.Strings(ids)
	response, err := json.Marshal(summaryResponse{Text: "Reviewed saved code and markup.", SourceIDs: ids})
	return model.Response{Text: string(response), InputTokens: 10}, err
}

func TestWorkerShrinksSerializedMarkupBeforeAppleTokenCountBoundary(t *testing.T) {
	hour := workerHour(t)
	hour.Evidence = nil
	for i := 0; i < 4; i++ {
		hour.Evidence = append(hour.Evidence, evidence(i+1, hour.Start.Add(time.Duration(i)*time.Minute), strings.Repeat("<>&", 1150)))
	}
	packed, err := PackHour(hour)
	if err != nil {
		t.Fatal(err)
	}
	client := &appleBoundaryCompletion{}
	g := &OllamaGenerator{client: client, schema: json.RawMessage(appleSummarySchema)}
	system, user, err := g.request(packed)
	if err != nil || len(system)+len(user) <= model.MaxRequestBytes {
		t.Fatalf("fixture does not cross Apple's serialized text boundary: bytes=%d err=%v", len(system)+len(user), err)
	}
	_, err = (&model.Apple{}).CountRequestTokens(context.Background(), model.Request{System: system, User: user, Format: "json", Schema: g.schema})
	if !errors.Is(err, model.ErrAppleConfig) {
		t.Fatalf("real Apple adapter unexpectedly accepted oversized fixture: %v", err)
	}
	repo := &workerRepo{rows: map[string]Summary{}}
	w := NewWorker(repo, &workerPolicy{value: balanced()}, g, WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer w.Close()
	if err := w.Process(context.Background(), hour); err != nil {
		t.Fatalf("markup recap failed before byte admission: %v", err)
	}
	if len(client.counts) != 1 || len(client.generated) != 1 {
		t.Fatalf("provider count/inference occurred before byte fit: %d/%d", len(client.counts), len(client.generated))
	}
	if len(client.counts[0].System)+len(client.counts[0].User) > model.MaxRequestBytes {
		t.Fatal("oversized request reached Apple token counter")
	}
	var payload summaryPayload
	if err := json.Unmarshal([]byte(client.generated[0].User), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Evidence) != 4 || payload.Coverage.Excerpted != 4 || payload.Coverage.Omitted != 0 {
		t.Fatalf("byte admission lost source coverage: %+v", payload.Coverage)
	}
	for i, item := range payload.Evidence {
		if item.SourceID != summarySourceLabel(i) || !item.Excerpted || len(item.ExcerptRanges) != 2 {
			t.Fatal("byte admission lost bound source label or excerpt ranges")
		}
	}
	row, found, err := repo.GetChronicle(context.Background(), packed.HourID)
	if err != nil || !found || !strings.Contains(row.Text, "4 source(s) excerpted") {
		t.Fatalf("byte admission coverage did not persist: %+v %t %v", row, found, err)
	}
	if strings.Join(row.SourceIDs, ",") != strings.Join(packed.SourceIDs, ",") {
		t.Fatal("short source labels changed original persisted provenance")
	}
}

func TestWorkerDoesNotRetryAppleConfigurationFailureAfterByteAdmission(t *testing.T) {
	client := &appleBoundaryCompletion{}
	g := &OllamaGenerator{client: client, schema: json.RawMessage(`{"type":`)}
	w := NewWorker(&workerRepo{rows: map[string]Summary{}}, &workerPolicy{value: balanced()}, g, WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer w.Close()
	if err := w.Process(context.Background(), workerHour(t)); !errors.Is(err, model.ErrAppleConfig) {
		t.Fatalf("genuine provider configuration error was changed: %v", err)
	}
	if len(client.counts) != 1 || len(client.generated) != 0 {
		t.Fatal("provider configuration error was retried or reached inference")
	}
}

type oversizedRequestGenerator struct {
	*admissionGenerator
	byteChecks int
}

func (g *oversizedRequestGenerator) FitsRequestBytes(Packed) (bool, error) {
	g.byteChecks++
	return false, nil
}

func TestWorkerBoundsUnavoidableByteOverheadWithoutCountingOrInference(t *testing.T) {
	hour := workerHour(t)
	hour.Evidence[0] = evidence(1, hour.Start, strings.Repeat("large code page ", 1000))
	g := &oversizedRequestGenerator{admissionGenerator: &admissionGenerator{maxAdmitted: 1664}}
	w := NewWorker(&workerRepo{rows: map[string]Summary{}}, &workerPolicy{value: balanced()}, g, WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer w.Close()
	if err := w.Process(context.Background(), hour); !errors.Is(err, ErrWorkerPolicy) {
		t.Fatalf("unavoidable byte overhead error=%v", err)
	}
	if g.byteChecks < 2 || g.byteChecks > 12 || g.counts != 0 || len(g.generated) != 0 {
		t.Fatalf("byte rejection was unbounded or reached provider: checks=%d counts=%d generated=%d", g.byteChecks, g.counts, len(g.generated))
	}
}
