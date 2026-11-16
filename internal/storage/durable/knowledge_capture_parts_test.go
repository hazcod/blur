package durable

import (
	"context"
	"github.com/hazcod/blur/internal/knowledge"
	"strings"
	"testing"
	"time"
)

func TestConfirmedKnowledgeAcceptsPublishedPartAndCascadesWholeCaptureDeletion(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	h, p, summary := makeStoredCapture(t, strings.Repeat("Northstar implementation discussion. ", 1400)+"Northstar is written in Go.", time.Now().UTC())
	stageStoredCapture(t, s, h, p)
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	for range 32 {
		more, err := s.IndexPendingCaptures(ctx, 16)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	ids, _, err := s.ListCaptureSourceIDs(ctx, summary.ContentID(), -1, 64)
	if err != nil || len(ids) < 2 {
		t.Fatal("part fixture", err)
	}
	id := ids[len(ids)-1]
	entity := knowledge.Entity{ID: "project-northstar", Kind: knowledge.Project, Name: "Northstar", State: knowledge.Confirmed, Facts: []knowledge.Fact{{ID: "fact-northstar", Text: "Northstar is written in Go.", State: knowledge.Confirmed, Sources: []knowledge.Source{{ID: id}}}}}
	if err = s.PutKnowledgeEntityFromSource(ctx, entity, id); err != nil {
		t.Fatal("confirmed part fact rejected", err)
	}
	if err = s.DeleteObservationRecord(ctx, id); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetKnowledgeEntity(ctx, entity.ID)
	if err != nil || !found || len(got.Facts) != 0 {
		t.Fatal("part fact survived source deletion", got, found, err)
	}
	if err = s.PutKnowledgeEntityFromSource(ctx, entity, id); err == nil {
		t.Fatal("late acceptance restored deleted part fact")
	}
}
