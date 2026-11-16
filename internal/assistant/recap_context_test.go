package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRecapSharesContextAcrossLongSourcesAndRetainsOriginals(t *testing.T) {
	for _, budget := range []int{8192, 4000} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			items := make([]Evidence, 8)
			for i := range items {
				items[i] = Evidence{SourceID: fmt.Sprintf("%064x", i+1), CapturedAt: time.Date(2026, 11, 13, i+8, 0, 0, 0, time.UTC), Text: fmt.Sprintf("PERIOD_%d ", i) + strings.Repeat("Saved work evidence. ", 400)}
			}
			g := &testGenerator{answer: ModelAnswer{Text: "Recorded work from the day.", SourceIDs: []string{items[0].SourceID}}}
			r := &recapContextRetriever{items: items, sampled: true}
			got, err := NewService(r, g, byteCounter{}, budget).Ask(context.Background(), "What did I do today?")
			if err != nil {
				t.Fatal(err)
			}
			if budget == 8192 && len(g.packed.SourceIDs) != 8 || budget == 4000 && len(g.packed.SourceIDs) < 3 {
				t.Fatalf("one long source crowded out other periods: sent=%d", len(g.packed.SourceIDs))
			}
			periods := []int{0, 7}
			if budget == 8192 {
				periods = []int{0, 1, 2, 3, 4, 5, 6, 7}
			}
			for _, i := range periods {
				if !strings.Contains(g.packed.User, fmt.Sprintf("PERIOD_%d", i)) {
					t.Fatalf("period %d absent from the actual provider payload", i)
				}
			}
			if len(got.Sources) != 1 || got.Sources[0].Text != items[0].Text || g.packed.InputBytes+MaxAnswerTokens+PromptTemplateReserve > budget {
				t.Fatal("source inspection or provider bound changed", got)
			}
			if !got.CoverageLimited {
				t.Fatal("limited recap was not disclosed to the answer surface")
			}
			var payload map[string]json.RawMessage
			if json.Unmarshal([]byte(g.packed.User), &payload) != nil || string(payload["sampled_time_window"]) != "true" {
				t.Fatal("recap sampling was hidden from the model", g.packed.User)
			}
		})
	}
}

func TestRecapCoverageDisclosureDoesNotDependOnTheModel(t *testing.T) {
	for _, sampled := range []bool{false, true} {
		r := &recapContextRetriever{items: []Evidence{{SourceID: strings.Repeat("a", 64), Text: "Saved a short note."}}, sampled: sampled}
		for _, generated := range []bool{false, true} {
			var g Generator
			if generated {
				g = &testGenerator{answer: ModelAnswer{Text: "Saved a short note.", SourceIDs: []string{strings.Repeat("a", 64)}}}
			}
			got, err := NewService(r, g, byteCounter{}, 8192).Ask(context.Background(), "What did I do today?")
			if err != nil || got.CoverageLimited != sampled {
				t.Fatalf("coverage state lost: generated=%t sampled=%t result=%+v err=%v", generated, sampled, got, err)
			}
		}
	}
}

func TestRecapWireAdmissionSharesEscapedUnicodeWithoutBreakingCitations(t *testing.T) {
	items := make([]Evidence, 8)
	for i := range items {
		items[i] = Evidence{SourceID: fmt.Sprintf("%064x", i+1), Text: strings.Repeat("界<>&\"🙂", 400), CapturedAt: time.Date(2026, 11, 10, i+8, 0, 0, 0, time.UTC)}
	}
	packed, err := packContextWithOptions("What did I do today?", items, nil, 6656, contextOptions{balanced: true, sampled: true})
	if err != nil || packed.InputBytes > 6656 || len(packed.SourceIDs) != 8 {
		t.Fatal("escaped text evicted other periods before fitting excerpts", packed, err)
	}
	var payload contextPayload
	if json.Unmarshal([]byte(packed.User), &payload) != nil || len(payload.Evidence) != 8 {
		t.Fatal("invalid serialized context")
	}
	for i, item := range payload.Evidence {
		if packed.SourceIDs[i] != items[i].SourceID || item.Text == "" || !item.Truncated || strings.Contains(packed.User, "source_id") {
			t.Fatal("excerpt/source map broke", payload)
		}
	}
}

func TestMultiDayRecapPreservesDateAndZonePerSummary(t *testing.T) {
	first := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	items := []Evidence{
		{SourceID: strings.Repeat("a", 64), Text: "Tuesday research", Layer: LayerHour, CapturedAt: first, EndAt: first.Add(time.Hour)},
		{SourceID: strings.Repeat("b", 64), Text: "Wednesday release", Layer: LayerHour, CapturedAt: first.AddDate(0, 0, 1), EndAt: first.AddDate(0, 0, 1).Add(time.Hour)},
	}
	g := &testGenerator{answer: ModelAnswer{Text: "Recorded research and release.", SourceIDs: []string{items[0].SourceID}}}
	_, err := NewService(&recapContextRetriever{items: items}, g, byteCounter{}, 8192).Ask(context.Background(), "What did I do this week?")
	if err != nil {
		t.Fatal(err)
	}
	var payload contextPayload
	if err := json.Unmarshal([]byte(g.packed.User), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Evidence) != 2 || payload.Evidence[0].Time != "2026-11-10T09:00:00Z–2026-11-10T10:00:00Z" || payload.Evidence[1].Time != "2026-11-11T09:00:00Z–2026-11-11T10:00:00Z" {
		t.Fatal("summary date or timezone lost before inference", payload)
	}
}

func TestMemoryPlannerUsesRetrieversReferenceTime(t *testing.T) {
	ref := time.Date(2026, 6, 1, 0, 30, 0, 0, time.FixedZone("fixture", 3600))
	r := &recapClockRetriever{recapContextRetriever: recapContextRetriever{items: []Evidence{{SourceID: strings.Repeat("a", 64), Text: "Recorded work."}}}, now: ref}
	_, err := NewService(r, nil, nil, 8192).Ask(context.Background(), "What did I do today?")
	if err != nil || r.plan.Scope.Start.Format(time.RFC3339) != "2026-06-01T00:00:00+01:00" || r.plan.Scope.End.Format(time.RFC3339) != "2026-06-01T00:30:00+01:00" {
		t.Fatalf("planner used a different day/zone than retrieval: %+v %v", r.plan.Scope, err)
	}
}

type recapClockRetriever struct {
	recapContextRetriever
	now  time.Time
	plan Plan
}

func (r *recapClockRetriever) AskTime() time.Time { return r.now }
func (r *recapClockRetriever) RetrievePlan(ctx context.Context, plan Plan) (Retrieval, error) {
	r.plan = plan
	return r.recapContextRetriever.RetrievePlan(ctx, plan)
}

type recapContextRetriever struct {
	items   []Evidence
	sampled bool
}

func (r *recapContextRetriever) Retrieve(context.Context, string) ([]Evidence, error) {
	return r.items, nil
}
func (r *recapContextRetriever) RetrievePlan(context.Context, Plan) (Retrieval, error) {
	return Retrieval{Evidence: r.items, Diagnosis: Diagnosis{RecapSampled: r.sampled}}, nil
}
