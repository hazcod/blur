package client

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/storage/durable"
)

func putDatedLibraryFact(t *testing.T, s *durable.Store, at time.Time, text string) capture.SanitizedObservation {
	t.Helper()
	o, err := capture.SanitizeObservation("editor.app", "work.example", text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(context.Background(), o, at); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyEnrichment(context.Background(), enrich.Chunk{SourceID: o.ContentID(), ModelKey: enrich.ModelKey("dated-library"), End: len(text), Next: len(text), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Person, Name: "Mira", Fact: text, SourceID: o.ContentID()}}}); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestDatedTopicLibraryRequiresEachFactsOwnObservedSupport(t *testing.T) {
	c, s, now := todayTaskClient(t)
	const old = "Mira approved the old parser release."
	const current = "Mira rejected the new parser release."
	putDatedLibraryFact(t, s, now.AddDate(0, 0, -7), old)
	putDatedLibraryFact(t, s, now.Add(-time.Minute), current)
	plan, err := assistant.PlanQuestion("What did we decide about Mira today?", now, nil)
	if err != nil || plan.Scope.IsZero() {
		t.Fatal(plan, err)
	}
	got, err := c.RetrievePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	var inferred strings.Builder
	for _, evidence := range got.Evidence {
		if evidence.Layer == assistant.LayerDigest {
			inferred.WriteString(evidence.Text)
		}
	}
	if strings.Contains(inferred.String(), old) || !strings.Contains(inferred.String(), current) {
		t.Fatalf("dated Library mixed historical and current claims: %q", inferred.String())
	}
	plan.Scope = assistant.Scope{}
	all, _, _, err := c.inferredTopicEvidence(context.Background(), plan)
	if err != nil || !hasFact(evidenceTexts(all), old) || !hasFact(evidenceTexts(all), current) {
		t.Fatal("dated filtering changed unbounded saved knowledge", all, err)
	}
}

func TestDatedTopicLibraryExcludesOldOnlyClaim(t *testing.T) {
	c, s, now := todayTaskClient(t)
	putDatedLibraryFact(t, s, now.AddDate(0, 0, -7), "Mira owns the vendor contract renewal.")
	plan, err := assistant.PlanQuestion("What did we decide about Mira today?", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.RetrievePlan(context.Background(), plan)
	if err != nil || len(got.Evidence) != 0 {
		t.Fatalf("old-only claim answered a dated question: %+v err=%v", got.Evidence, err)
	}
}

func TestDatedLibraryUsesObservedRevisitInsideLongScope(t *testing.T) {
	c, s, now := todayTaskClient(t)
	const fact = "Mira validated the parser checkpoint."
	o := putDatedLibraryFact(t, s, now.AddDate(-2, 0, 0), fact)
	visit := now.AddDate(0, -6, 0)
	if err := s.RecordObservationSighting(context.Background(), o, visit); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordObservationSighting(context.Background(), o, now); err != nil {
		t.Fatal(err)
	}
	plan := assistant.Plan{Kind: assistant.KindLookup, Question: "What did Mira validate?", Terms: []string{"mira"}, Scope: assistant.Scope{Start: now.AddDate(-1, 0, 0), End: now.AddDate(0, -1, 0), Explicit: true}}
	got, _, _, err := c.inferredTopicEvidence(context.Background(), plan)
	if err != nil || !hasFact(evidenceTexts(got), fact) {
		t.Fatal("dated knowledge lost an in-range revisit or exceeded a physical month", got, err)
	}
}

func TestDatedLibraryScopesCorrectionsOpenLoopsAndLinkedSourcesWithoutMutation(t *testing.T) {
	c, s, now := todayTaskClient(t)
	old := putDatedLibraryFact(t, s, now.AddDate(0, 0, -7), "Mira approved the old parser.")
	current := putDatedLibraryFact(t, s, now.Add(-time.Minute), "Mira requested a parser retest.")
	oldSource := []knowledge.Source{{ID: old.ContentID()}}
	currentSource := []knowledge.Source{{ID: current.ContentID()}}
	user := []knowledge.Source{{UserAuthored: true}}
	entity := knowledge.Entity{ID: "dated", Name: "Mira", Facts: []knowledge.Fact{
		{ID: "old", Text: "old", Sources: oldSource},
		{ID: "current", Text: "current", Sources: currentSource},
		{ID: "correct-old", Text: "correct old", Sources: user, Supersedes: []string{old.ContentID()}},
		{ID: "correct-current", Text: "correct current", Sources: user, Supersedes: []string{current.ContentID()}},
		{ID: "undated", Text: "undated authored text", Sources: user},
		{ID: "mixed", Text: "same fact supported more than once", Sources: append(slices.Clone(oldSource), currentSource...)},
	}, OpenLoops: []knowledge.OpenLoop{
		{ID: "old-loop", Text: "old loop", Sources: oldSource},
		{ID: "current-loop", Text: "current loop", Sources: currentSource},
		{ID: "undated-loop", Text: "undated loop", Sources: user},
	}, ActivitySourceIDs: []string{old.ContentID(), current.ContentID()}}
	plan := assistant.Plan{Scope: assistant.Scope{Start: now.Add(-time.Hour), End: now, Explicit: true}}
	got, err := c.datedLibraryEntities(context.Background(), plan, []knowledge.Entity{entity})
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	var ids []string
	for _, fact := range got[0].Facts {
		ids = append(ids, fact.ID)
	}
	if !slices.Equal(ids, []string{"current", "correct-current", "mixed"}) || len(got[0].OpenLoops) != 1 || got[0].OpenLoops[0].ID != "current-loop" || !slices.Equal(got[0].ActivitySourceIDs, []string{current.ContentID()}) {
		t.Fatal("dated projection mixed unrelated provenance", got[0])
	}
	if len(entity.Facts) != 6 || len(entity.OpenLoops) != 3 || len(entity.ActivitySourceIDs) != 2 {
		t.Fatal("projection mutated saved slices", entity)
	}
	if err := s.DeleteObservationRecord(context.Background(), current.ContentID()); err != nil {
		t.Fatal(err)
	}
	got, err = c.datedLibraryEntities(context.Background(), plan, []knowledge.Entity{entity})
	if err != nil || len(got) != 0 {
		t.Fatal("deleted support remained dated evidence", got, err)
	}
}

func TestDatedLibraryCancellationDoesNotReturnSuccessfulPartialProjection(t *testing.T) {
	c, s, now := todayTaskClient(t)
	o := putDatedLibraryFact(t, s, now.Add(-time.Minute), "Mira requested the parser retest.")
	entity := knowledge.Entity{ID: "dated", Facts: []knowledge.Fact{{ID: "fact", Sources: []knowledge.Source{{ID: o.ContentID()}}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := c.datedLibraryEntities(ctx, assistant.Plan{Scope: assistant.Scope{Start: now.Add(-time.Hour), End: now}}, []knowledge.Entity{entity})
	if !errors.Is(err, context.Canceled) || len(got) != 0 {
		t.Fatal("canceled scope lookup published a projection", got, err)
	}
}

type datedSightingProbe struct {
	*durable.Store
	unknown time.Time
	calls   int
}

func (p *datedSightingProbe) IsObservationTimeKnown(at time.Time) bool {
	return !at.Equal(p.unknown) && p.Store.IsObservationTimeKnown(at)
}
func (p *datedSightingProbe) SourceEvidenceSightingAt(ctx context.Context, id string, start, end time.Time) (time.Time, bool, error) {
	p.calls++
	if !p.unknown.IsZero() && !p.unknown.Before(start) && p.unknown.Before(end) {
		return p.unknown, true, nil
	}
	return p.Store.SourceEvidenceSightingAt(ctx, id, start, end)
}

func TestDatedLibraryLongRangeUsesKnownCaptureWithoutWalkingAllMonths(t *testing.T) {
	c, s, now := todayTaskClient(t)
	putDatedLibraryFact(t, s, now.AddDate(-5, 0, 0), "Mira validated the original parser.")
	probe := &datedSightingProbe{Store: s}
	c.data = probe
	plan := assistant.Plan{Kind: assistant.KindLookup, Question: "Mira parser", Terms: []string{"mira"}, Scope: assistant.Scope{Start: now.AddDate(-10, 0, 0), End: now, Explicit: true}}
	got, _, _, err := c.inferredTopicEvidence(context.Background(), plan)
	if err != nil || len(got) == 0 || probe.calls != 0 {
		t.Fatal("known in-range support unnecessarily walked monthly history", len(got), probe.calls, err)
	}
}

func TestDatedLibraryUnknownLatestVisitDoesNotHideKnownEarlierVisit(t *testing.T) {
	c, s, now := todayTaskClient(t)
	o := putDatedLibraryFact(t, s, now.AddDate(-1, 0, 0), "Mira validated the parser retry.")
	if err := s.RecordObservationSighting(context.Background(), o, now.Add(-20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	probe := &datedSightingProbe{Store: s, unknown: now.Add(-10 * time.Minute)}
	c.data = probe
	plan := assistant.Plan{Kind: assistant.KindLookup, Question: "Mira parser", Terms: []string{"mira"}, Scope: assistant.Scope{Start: now.Add(-time.Hour), End: now, Explicit: true}}
	got, _, _, err := c.inferredTopicEvidence(context.Background(), plan)
	if err != nil || len(got) == 0 || probe.calls != 2 {
		t.Fatal("unknown latest visit hid earlier observed support", len(got), probe.calls, err)
	}
}

type datedLegacyCorrectionReader struct {
	*durable.Store
	entity knowledge.Entity
}

func (r *datedLegacyCorrectionReader) FindInferredKnowledgeByNamesInScope(context.Context, []string, int, []string, []string) ([]knowledge.Entity, bool, error) {
	return []knowledge.Entity{r.entity}, false, nil
}

func TestDatedLibraryDoesNotRestoreSourceSupersededByUndatedLegacyCorrection(t *testing.T) {
	c, s, now := todayTaskClient(t)
	const old = "Mira approved the parser release."
	o := putDatedLibraryFact(t, s, now.Add(-time.Minute), old)
	entities, err := s.FindInferredKnowledge(context.Background(), "Mira")
	if err != nil || len(entities) != 1 {
		t.Fatal(entities, err)
	}
	if err := s.CorrectInferredFact(context.Background(), entities[0].ID, entities[0].Facts[0].ID, "Mira revoked the parser release."); err != nil {
		t.Fatal(err)
	}
	entities, err = s.FindInferredKnowledge(context.Background(), "Mira")
	if err != nil || len(entities) != 1 {
		t.Fatal(entities, err)
	}
	// Legacy corrections predate the per-fact supersession list. Their
	// existing suppression authority is retained without inventing a date.
	entities[0].Facts[0].Supersedes = nil
	c.data = &datedLegacyCorrectionReader{Store: s, entity: entities[0]}
	plan := assistant.Plan{Kind: assistant.KindLookup, Question: "What did we decide about Mira today?", Terms: []string{"mira"}, Scope: assistant.Scope{Start: now.Add(-time.Hour), End: now, Explicit: true}}
	evidence, _, superseded, err := c.inferredTopicEvidence(context.Background(), plan)
	if err != nil || len(evidence) != 0 || !slices.Contains(superseded, o.ContentID()) {
		t.Fatal("undated correction lost its original-source suppression", evidence, superseded, err)
	}
}
