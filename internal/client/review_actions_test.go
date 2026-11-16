package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
)

func TestTodayDismissActionRefreshesWithoutCreatingTask(t *testing.T) {
	c, s, _ := todayTaskClient(t)
	if _, err := c.AddNote("TODO: Review Atlas"); err != nil {
		t.Fatal(err)
	}
	view, err := c.Today("2026-11-12")
	if err != nil || len(view.Snapshot.TodoCandidates) != 1 {
		t.Fatal(view, err)
	}
	app := &taskInterleaveApp{posted: make(chan func(), 8)}
	d := &desktop{client: c, app: app, page: "Today", date: view.Date, today: view}
	text := mountedLibraryText(t, d.todayView())
	if !strings.Contains(text, "Dismiss") || !strings.Contains(text, "Add task") {
		t.Fatal("missing separate review actions", text)
	}
	d.dismissTodayTask(view.Snapshot.TodoCandidates[0].ID)
	for d.loading || d.todayLoading || len(d.today.Snapshot.TodoCandidates) != 0 {
		select {
		case callback := <-app.posted:
			callback()
		case <-time.After(5 * time.Second):
			t.Fatal("dismiss/refresh did not finish")
		}
	}
	if d.loading || len(d.today.Snapshot.TodoCandidates) != 0 {
		t.Fatal("dismissed suggestion remains visible", d.today)
	}
	page, err := c.TaskPage(context.Background(), "", "")
	if err != nil || len(page.Items) != 0 {
		t.Fatal("dismiss created task", page, err)
	}
	stored, _, err := s.GetTodaySnapshot(context.Background(), view.Date)
	if err != nil || len(stored.DismissedTodos) != 1 {
		t.Fatal("no durable dismissal", stored, err)
	}
}

func TestReviewActionsRejectClickFromPreviousMemoryEpoch(t *testing.T) {
	for _, action := range []string{"dismiss-task", "confirm-fact"} {
		t.Run(action, func(t *testing.T) {
			c, s, at := todayTaskClient(t)
			if _, err := c.AddNote("TODO: Review Atlas"); err != nil {
				t.Fatal(err)
			}
			view, err := c.Today("2026-11-12")
			if err != nil {
				t.Fatal(err)
			}
			source, _ := capture.SanitizeObservation("editor.app", "code.example", "Atlas uses Go.", 4096)
			if err := s.PutObservationAt(context.Background(), source, at); err != nil {
				t.Fatal(err)
			}
			chunk := enrich.Chunk{SourceID: source.ContentID(), ModelKey: enrich.ModelKey("fixture"), End: len(source.Text()), Next: len(source.Text()), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Project, Name: "Atlas", Fact: source.Text(), SourceID: source.ContentID()}}}
			if err := s.ApplyEnrichment(context.Background(), chunk); err != nil {
				t.Fatal(err)
			}
			entityID := enrich.EntityID(knowledge.Project, "Atlas", source.AppID(), source.Domain())
			entity, _, _ := s.GetInferredKnowledge(context.Background(), entityID)
			app := &taskInterleaveApp{posted: make(chan func(), 1), update: func() { c.NotifyMemoryChanging(); c.NotifyMemoryStable() }}
			d := &desktop{client: c, app: app, today: view}
			if action == "dismiss-task" {
				d.dismissTodayTask(view.Snapshot.TodoCandidates[0].ID)
			} else {
				d.confirmLibraryFact(entityID, entity.Facts[0])
			}
			select {
			case <-app.posted:
			case <-time.After(5 * time.Second):
				t.Fatal("review action did not finish")
			}
			stored, _, err := s.GetTodaySnapshot(context.Background(), view.Date)
			if err != nil || len(stored.DismissedTodos) != 0 || len(stored.TodoCandidates) != 1 {
				t.Fatal("stale click dismissed task", stored, err)
			}
			entity, _, err = s.GetInferredKnowledge(context.Background(), entityID)
			if err != nil || entity.Facts[0].State != knowledge.Proposed {
				t.Fatal("stale click confirmed fact", entity, err)
			}
		})
	}
}

func TestLibraryConfirmActionPreservesSourceAndReplacesReviewControl(t *testing.T) {
	c, s, at := todayTaskClient(t)
	source, _ := capture.SanitizeObservation("editor.app", "code.example", "Atlas uses Go.", 4096)
	if err := s.PutObservationAt(context.Background(), source, at); err != nil {
		t.Fatal(err)
	}
	chunk := enrich.Chunk{SourceID: source.ContentID(), ModelKey: enrich.ModelKey("fixture"), End: len(source.Text()), Next: len(source.Text()), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Project, Name: "Atlas", Fact: source.Text(), SourceID: source.ContentID()}}}
	if err := s.ApplyEnrichment(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	id := enrich.EntityID(knowledge.Project, "Atlas", source.AppID(), source.Domain())
	entity, _, _ := s.GetInferredKnowledge(context.Background(), id)
	app := &taskInterleaveApp{posted: make(chan func(), 8)}
	d := &desktop{client: c, app: app, page: "Library", libraryInspect: &entity}
	if text := mountedLibraryText(t, d.libraryFactControls(id, entity.Facts[0])); !strings.Contains(text, "Confirm") {
		t.Fatal("no explicit confirm control", text)
	}
	d.confirmLibraryFact(id, entity.Facts[0])
	for i := 0; i < 3; i++ {
		select {
		case callback := <-app.posted:
			callback()
		case <-time.After(5 * time.Second):
			t.Fatal("confirm/refresh did not finish")
		}
	}
	if d.libraryInspect == nil || len(d.libraryInspect.Facts) != 1 {
		t.Fatal("confirmed detail was not reloaded")
	}
	fact := d.libraryInspect.Facts[0]
	if fact.State != knowledge.Confirmed || fact.Text != source.Text() || len(fact.Sources) != 1 || fact.Sources[0].ID != source.ContentID() || fact.Sources[0].UserAuthored {
		t.Fatal("confirmation rewrote source", fact)
	}
	if text := mountedLibraryText(t, d.libraryFactControls(id, fact)); strings.Contains(text, "Confirm") {
		t.Fatal("confirmed detail remains reviewable", text)
	}
	page, err := c.TaskPage(context.Background(), "", "")
	if err != nil || len(page.Items) != 0 {
		t.Fatal("fact confirmation created task", page, err)
	}
}
