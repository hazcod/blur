package durable

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/tasks"
)

func TestNoteRevisionInferenceRetiresObsoleteClaimsBeforeCapacityBlocksLaterSources(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xb6)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	var doc notes.Document
	var firstChunk enrich.Chunk
	var sources []string
	var confirmed knowledge.Fact
	item := taskFixture(99)
	item.UserAuthored = false
	item.Reminder = &tasks.Reminder{DueAt: at.Add(24 * time.Hour), Timezone: "UTC"}
	item.Delivery = tasks.Delivery{State: tasks.Scheduled, At: at, Attempted: true}
	names := []string{"Atlas", "Beacon", "Orion"}
	for revision := 1; revision <= knowledge.MaxFacts+1; revision++ {
		var lines []string
		for _, name := range names {
			lines = append(lines, fmt.Sprintf("%s revision %d uses Go.", name, revision))
		}
		text := strings.Join(lines, " ")
		if revision == 1 {
			doc, err = s.CreateNoteDocument(ctx, text, at)
		} else {
			doc, err = s.UpdateNoteDocument(ctx, doc.ID, doc.Revision, text, at.Add(time.Duration(revision)*time.Minute))
		}
		if err != nil {
			t.Fatalf("revision %d edit: %v", revision, err)
		}
		sources = append(sources, doc.SourceID)
		chunk := enrich.Chunk{SourceID: doc.SourceID, ModelKey: enrich.ModelKey("fixture"), End: len(text), Next: len(text), Complete: true}
		for i, name := range names {
			chunk.Proposals = append(chunk.Proposals, derive.Proposal{Kind: knowledge.Project, Name: name, Fact: lines[i], SourceID: doc.SourceID})
		}
		if err := s.ApplyEnrichment(ctx, chunk); err != nil {
			t.Fatalf("revision %d inference: %v", revision, err)
		}
		if revision == 1 {
			firstChunk = chunk
			id := enrich.EntityID(knowledge.Project, "Atlas", "sh.blur.notes", "local")
			entity, _, err := s.GetInferredKnowledge(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			confirmed = entity.Facts[0]
			if err := s.ConfirmInferredFact(ctx, id, confirmed); err != nil {
				t.Fatal(err)
			}
			item.SourceID = doc.SourceID
			if err := s.PutTask(ctx, item, 0); err != nil {
				t.Fatal(err)
			}
			item, _, err = s.GetTask(ctx, item.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	// Three subjects at the real limit require bounded independent repair,
	// without starving a different source behind this revision's chunk.
	unrelated := enrichmentFixture(t, s, "work.example", "Vega ships Thursday.", derive.Proposal{Kind: knowledge.Project, Name: "Vega", Fact: "Vega ships Thursday."})
	if err := s.ApplyEnrichment(ctx, unrelated); err != nil {
		t.Fatal("later source blocked", err)
	}
	if progress, found, err := s.GetEnrichmentProgress(ctx, unrelated.SourceID); err != nil || !found || !progress.Complete {
		t.Fatal("later source did not advance", progress, found, err)
	}
	if err := s.ApplyEnrichment(ctx, firstChunk); !errors.Is(err, enrich.ErrSourceGone) {
		t.Fatal("late obsolete inference accepted", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		id := enrich.EntityID(knowledge.Project, name, "sh.blur.notes", "local")
		entity, found, err := s.GetInferredKnowledge(ctx, id)
		if err != nil || !found || len(entity.Facts) != 1 || entity.Facts[0].Sources[0].ID != doc.SourceID {
			t.Fatal("current subject lost", name, entity, found, err)
		}
		var raw inferredRecord
		if err := s.view(ctx, func(r bolt.Reader) error { var err error; raw, _, err = s.readInferredRecord(r, id); return err }); err != nil {
			t.Fatal(err)
		}
		retired := 0
		for _, sourceID := range sources[:len(sources)-1] {
			if slices.Contains(raw.Sources, sourceID) {
				continue
			}
			retired++
			progress, found, err := s.GetEnrichmentProgress(ctx, sourceID)
			if err != nil || !found || !progress.Complete || slices.Contains(progress.Entities, id) {
				t.Fatal("retired claim left a broken reverse receipt", progress, found, err)
			}
		}
		if retired == 0 {
			t.Fatal("obsolete claims still consume the capacity", name)
		}
		if name == "Atlas" {
			found := false
			for _, fact := range raw.Entity.Facts {
				if fact.ID == confirmed.ID {
					found = true
					if fact.State != knowledge.Confirmed || fact.Text != confirmed.Text || !slices.Equal(fact.Sources, confirmed.Sources) {
						t.Fatal("explicit fact confirmation changed", fact)
					}
				}
			}
			if !found {
				t.Fatal("explicit confirmation retired")
			}
		}
	}
	if saved, found, err := s.GetTask(ctx, item.ID); err != nil || !found || !reflect.DeepEqual(saved, item) {
		t.Fatal("retirement changed task or native delivery intent", saved, found, err)
	}
	if proof, found, err := s.GetObservation(ctx, sources[0]); err != nil || !found || !strings.Contains(proof.Text(), "revision 1") {
		t.Fatal("immutable proof lost", found, err)
	}
	if err := s.DeleteNoteDocument(ctx, doc.ID, doc.Revision); err != nil {
		t.Fatal("whole-note erasure failed after retirement", err)
	}
	for _, sourceID := range sources {
		if _, found, err := s.GetEnrichmentProgress(ctx, sourceID); err != nil || found {
			t.Fatal("erasure left progress", sourceID, found, err)
		}
	}
	for _, name := range names {
		if _, found, err := s.GetInferredKnowledge(ctx, enrich.EntityID(knowledge.Project, name, "sh.blur.notes", "local")); err != nil || found {
			t.Fatal("erasure left subject", name, found, err)
		}
	}
}
