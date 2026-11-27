package client

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

const directOriginalOwnership = "Mira owns the vendor contract renewal."
const directCorrectedOwnership = "Mira no longer owns the vendor contract; Alex owns it."

func directEntityCorrectionSource(t *testing.T, store *durable.Store, name, app, domain, text string, at time.Time) (string, knowledge.Entity) {
	t.Helper()
	ctx := context.Background()
	o, err := capture.SanitizeObservation(app, domain, text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, o, at); err != nil {
		t.Fatal(err)
	}
	return o.ContentID(), directEntityEnrichSource(t, store, name, o.ContentID(), text)
}

func directEntityEnrichSource(t *testing.T, store *durable.Store, name, sourceID, text string) knowledge.Entity {
	t.Helper()
	ctx := context.Background()
	if err := store.ApplyEnrichment(ctx, enrich.Chunk{SourceID: sourceID, ModelKey: enrich.ModelKey("direct-entity-correction"), End: len(text), Next: len(text), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Person, Name: name, Fact: text, SourceID: sourceID}}}); err != nil {
		t.Fatal(err)
	}
	entities, err := store.FindInferredKnowledge(ctx, name)
	if err != nil || len(entities) != 1 || len(entities[0].Facts) != 1 {
		t.Fatal("missing inferred fixture", entities, err)
	}
	return entities[0]
}

func TestDirectEntitySummaryHonorsSavedCorrectionOfflineAndApple(t *testing.T) {
	for _, apple := range []bool{false, true} {
		t.Run(map[bool]string{false: "offline", true: "apple"}[apple], func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "memory.db")
			key := securestore.OfflineFixtureKey()
			store, err := durable.OpenDurable(path, key, durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			original, entity := directEntityCorrectionSource(t, store, "Mira", "editor.app", "work.example", directOriginalOwnership, time.Now().Add(-time.Hour))
			if err := store.CorrectInferredFact(ctx, entity.ID, entity.Facts[0].ID, directCorrectedOwnership); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = durable.OpenDurable(path, key, durable.OpenExisting)
			if err != nil {
				t.Fatal(err)
			}
			c := NewClient(store)
			defer c.Close()
			model := &singleSearchModel{answer: assistant.ModelAnswer{InsufficientEvidence: true}}
			if apple {
				c.localAI = &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}, askModel: model, service: notes.NewService(store, nil, nil)}
			}
			got, err := c.AskMemory(ctx, "What do you remember about Mira?")
			if err != nil || !got.Direct || got.Answer.InsufficientEvidence || !strings.Contains(got.Answer.Text, directCorrectedOwnership) || strings.Contains(got.Answer.Text, directOriginalOwnership) || len(got.Sources) != 1 || got.Sources[0].SourceID == original || got.Sources[0].Origin != assistant.OriginUserCorrection || model.answerCalls != 0 || model.calls != 0 {
				t.Fatalf("direct entity ignored correction authority: direct=%t answer=%q sources=%+v model-calls=%d/%d err=%v", got.Direct, got.Answer.Text, got.Sources, model.answerCalls, model.calls, err)
			}
			page, err := store.SearchExact(ctx, search.ExactQuery{Terms: []string{"mira"}, TopK: 3, MaxRows: 48, MaxBytes: search.MaxIndexBytes})
			if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != original {
				t.Fatal("correction changed the authoritative searchable original", page, err)
			}
		})
	}
}

func TestDirectEntitySummaryDoesNotReviveDismissedFact(t *testing.T) {
	c := libraryNameFixture(t, "Mira")
	store := c.data.(*durable.Store)
	ctx := context.Background()
	entities, err := store.FindInferredKnowledge(ctx, "Mira")
	if err != nil || len(entities) != 1 {
		t.Fatal(entities, err)
	}
	if err := store.DismissInferredFact(ctx, entities[0].ID, entities[0].Facts[0].ID); err != nil {
		t.Fatal(err)
	}
	got, err := c.AskMemory(ctx, "What do you remember about Mira?")
	if err != nil || !got.Direct || !got.Answer.InsufficientEvidence || len(got.Sources) != 0 || strings.Contains(got.Answer.Text, directOriginalOwnership) {
		t.Fatalf("direct/fallback resurrected dismissed text: direct=%t insufficient=%t answer=%q sources=%d err=%v", got.Direct, got.Answer.InsufficientEvidence, got.Answer.Text, len(got.Sources), err)
	}
}

func TestDirectEntityCorrectionKeepsFoldedNameAndScope(t *testing.T) {
	ctx := context.Background()
	store := foldAskStore(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	_, entity := directEntityCorrectionSource(t, store, "José", "editor.app", "work.example", "José owns the vendor contract renewal.", now.Add(-time.Hour))
	const correction = "José no longer owns the contract; Alex owns it."
	if err := store.CorrectInferredFact(ctx, entity.ID, entity.Facts[0].ID, correction); err != nil {
		t.Fatal(err)
	}
	c := NewClient(store)
	defer c.Close()
	c.askNow = func() time.Time { return now }
	base := assistant.Plan{Kind: assistant.KindEntity, Entity: "Jose", Question: "What do you know about Jose?", Terms: []string{"jose"}}
	for _, tc := range []struct {
		name        string
		apps, hosts []string
		start, end  time.Time
		want        bool
	}{
		{name: "folded", want: true},
		{name: "matching-context", apps: []string{"editor.app"}, hosts: []string{"work.example"}, want: true},
		{name: "other-app", apps: []string{"com.apple.mail"}},
		{name: "other-domain", hosts: []string{"other.example"}},
		{name: "supported-date", start: now.Add(-2 * time.Hour), end: now, want: true},
		{name: "outside-date", start: now.Add(-30 * time.Minute), end: now},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := base
			plan.AppIDs, plan.Domains = tc.apps, tc.hosts
			plan.Scope = assistant.Scope{Start: tc.start, End: tc.end}
			got, _, err := c.AnswerDirect(ctx, plan)
			if err != nil || strings.Contains(got.Text, correction) != tc.want || strings.Contains(got.Text, "José owns the vendor contract renewal.") {
				t.Fatalf("correction escaped its exact subject/context/date: answer=%q want=%t err=%v", got.Text, tc.want, err)
			}
		})
	}
}

func TestDirectEntityCorrectionDoesNotOverrideAnotherAppContext(t *testing.T) {
	ctx := context.Background()
	store := foldAskStore(t)
	_, entity := directEntityCorrectionSource(t, store, "Mira", "editor.app", "work.example", directOriginalOwnership, time.Now().Add(-time.Hour))
	if err := store.CorrectInferredFact(ctx, entity.ID, entity.Facts[0].ID, directCorrectedOwnership); err != nil {
		t.Fatal(err)
	}
	const other = "Mira coordinates the finance review."
	o, err := capture.SanitizeObservation("com.apple.mail", "other.example", other, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, o, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	c := NewClient(store)
	defer c.Close()
	plan := assistant.Plan{Kind: assistant.KindEntity, Entity: "Mira", Question: "What do you know about Mira?", AppIDs: []string{"com.apple.mail"}, Domains: []string{"other.example"}}
	got, handled, err := c.AnswerDirect(ctx, plan)
	if err != nil || !handled || !strings.Contains(got.Text, other) || strings.Contains(got.Text, directCorrectedOwnership) || len(got.Sources) != 1 || got.Sources[0].SourceID != o.ContentID() {
		t.Fatalf("one context's correction replaced another context: answer=%q sources=%+v err=%v", got.Text, got.Sources, err)
	}
}

func TestDirectEntityCorrectionSuppressesOriginalWhenLegacyCorrectionIsUndated(t *testing.T) {
	ctx := context.Background()
	store := foldAskStore(t)
	now := time.Now()
	_, entity := directEntityCorrectionSource(t, store, "Mira", "editor.app", "work.example", directOriginalOwnership, now.Add(-time.Minute))
	if err := store.CorrectInferredFact(ctx, entity.ID, entity.Facts[0].ID, directCorrectedOwnership); err != nil {
		t.Fatal(err)
	}
	entities, err := store.FindInferredKnowledge(ctx, "Mira")
	if err != nil || len(entities) != 1 {
		t.Fatal(entities, err)
	}
	entities[0].Facts[0].Supersedes = nil
	c := NewClient(&datedLegacyCorrectionReader{Store: store, entity: entities[0]})
	defer c.Close()
	plan := assistant.Plan{Kind: assistant.KindEntity, Entity: "Mira", Question: "What do you remember about Mira?", Scope: assistant.Scope{Start: now.Add(-time.Hour), End: now}}
	got, handled, err := c.AnswerDirect(ctx, plan)
	if err != nil || !handled || !got.InsufficientEvidence || len(got.Sources) != 0 || strings.Contains(got.Text, directOriginalOwnership) || strings.Contains(got.Text, directCorrectedOwnership) {
		t.Fatalf("excluded undated correction revived its old capture: handled=%t answer=%q sources=%d err=%v", handled, got.Text, len(got.Sources), err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	got, _, err = c.AnswerDirect(canceled, plan)
	if !errors.Is(err, context.Canceled) || len(got.Sources) != 0 {
		t.Fatalf("canceled correction lookup returned evidence: sources=%d err=%v", len(got.Sources), err)
	}
}

func TestDirectEntityCorrectionPreservesNoteRevisionAndDeletionGates(t *testing.T) {
	ctx := context.Background()
	store := foldAskStore(t)
	c := NewClient(store)
	defer c.Close()
	note, err := store.CreateNoteDocument(ctx, directOriginalOwnership, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	entity := directEntityEnrichSource(t, store, "Mira", note.SourceID, directOriginalOwnership)
	if err := store.CorrectInferredFact(ctx, entity.ID, entity.Facts[0].ID, directCorrectedOwnership); err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateNoteDocument(ctx, note.ID, note.Revision, "The current note is about a different project.", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, deleted := range []bool{false, true} {
		if deleted {
			if err := store.DeleteNoteDocument(ctx, updated.ID, updated.Revision); err != nil {
				t.Fatal(err)
			}
		}
		got, err := c.AskMemory(ctx, "What do you remember about Mira?")
		if err != nil || strings.Contains(got.Answer.Text, directOriginalOwnership) {
			t.Fatalf("old note text reappeared after revision/deletion: deleted=%t answer=%q err=%v", deleted, got.Answer.Text, err)
		}
		for _, source := range got.Sources {
			if source.SourceID == note.SourceID || source.SourceID == updated.SourceID {
				t.Fatal("historical/deleted note granted a current citation", deleted, source.SourceID)
			}
		}
	}
}

func TestDirectEntityCorrectionReportsSampledContextsAndPendingIndex(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint("pending=", pending), func(t *testing.T) {
			ctx := context.Background()
			store := foldAskStore(t)
			for i := 0; i < 4; i++ {
				domain := fmt.Sprintf("team%d.example", i)
				original := fmt.Sprintf("Mira owns contract %d.", i)
				o, err := capture.SanitizeObservation("editor.app", domain, original, 4096)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.PutObservationAt(ctx, o, time.Now().Add(-time.Hour)); err != nil {
					t.Fatal(err)
				}
				if err := store.ApplyEnrichment(ctx, enrich.Chunk{SourceID: o.ContentID(), ModelKey: enrich.ModelKey("direct-contexts"), End: len(original), Next: len(original), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Person, Name: "Mira", Fact: original, SourceID: o.ContentID()}}}); err != nil {
					t.Fatal(err)
				}
				entities, err := store.FindInferredKnowledge(ctx, "Mira")
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, entity := range entities {
					if entity.Inference != nil && entity.Inference.Domain == domain {
						found = true
						if err := store.CorrectInferredFact(ctx, entity.ID, entity.Facts[0].ID, fmt.Sprintf("Mira no longer owns contract %d; Alex owns it.", i)); err != nil {
							t.Fatal(err)
						}
					}
				}
				if !found {
					t.Fatal("missing independent correction context", domain)
				}
			}
			if pending {
				var body strings.Builder
				for i := 0; i <= search.InlineObservationTerms; i++ {
					fmt.Fprintf(&body, "term%03d ", i)
				}
				o, err := capture.SanitizeObservation("editor.app", "pending.example", body.String(), 4096)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.PutObservation(ctx, o); err != nil {
					t.Fatal(err)
				}
			}
			if actual, err := store.ExactIndexPending(ctx); err != nil || actual != pending {
				t.Fatal("invalid pending fixture", actual, err)
			}
			c := NewClient(store)
			defer c.Close()
			got, err := c.AskMemory(ctx, "What do you remember about Mira?")
			if err != nil || !got.Direct || len(got.Sources) != 3 || !got.CoverageLimited || got.IndexingPending != pending || strings.Contains(got.Answer.Text, "Mira owns contract") {
				t.Fatalf("sampled direct answer hid coverage: direct=%t sources=%d limited=%t pending=%t want-pending=%t answer=%q err=%v", got.Direct, len(got.Sources), got.CoverageLimited, got.IndexingPending, pending, got.Answer.Text, err)
			}
		})
	}
}
