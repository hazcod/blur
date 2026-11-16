package durable

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/tasks"
)

func TestTodayDismissalSurvivesRefreshReopenAndErasesWithSource(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xd2)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	source, _ := capture.SanitizeObservation("sh.blur.notes", "local", "TODO: Review Atlas", 4096)
	if err := s.SaveNote(ctx, source, at); err != nil {
		t.Fatal(err)
	}
	proposal := notes.Proposal{ID: "atlas", Kind: notes.Task, Text: "Review Atlas", SourceID: source.ContentID(), CreatedAt: at}
	if err := s.SaveProposals(ctx, source.ContentID(), []notes.Proposal{proposal}); err != nil {
		t.Fatal(err)
	}
	stale, _, err := s.GetTodaySnapshot(ctx, "2026-11-12")
	if err != nil || len(stale.TodoCandidates) != 1 {
		t.Fatal(stale, err)
	}
	accepted := tasks.TodayCandidate{Date: stale.Date, ID: proposal.ID, Text: proposal.Text, SourceIDs: []string{source.ContentID()}}
	wrong := accepted
	wrong.Text = "A changed suggestion"
	if err := s.DismissTodayTask(ctx, wrong); !errors.Is(err, tasks.ErrConflict) {
		t.Fatal("dismissed a different preview", err)
	}
	if err := s.DismissTodayTask(ctx, accepted); err != nil {
		t.Fatal(err)
	}
	if err := s.DismissTodayTask(ctx, accepted); err != nil {
		t.Fatal("retry", err)
	}
	// A generation with no candidate must not forget the dismissal receipt.
	empty := stale.Clone()
	empty.TodoCandidates, empty.RecentChanges, empty.SourceIDs = nil, nil, nil
	if err := s.PutTodaySnapshot(ctx, empty); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTodaySnapshot(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProposals(ctx, source.ContentID(), []notes.Proposal{proposal}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := s.GetTodaySnapshot(ctx, stale.Date)
	if err != nil || len(current.TodoCandidates) != 0 || len(current.DismissedTodos) != 1 {
		t.Fatal("dismissal lost", current, err)
	}
	page, err := s.ListTasks(ctx, tasks.PageRequest{Limit: 32})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("dismiss created a task", page, err)
	}
	if _, found, err := s.GetObservation(ctx, source.ContentID()); err != nil || !found {
		t.Fatal("dismiss removed source", found, err)
	}
	if _, err := s.ConfirmTodayTask(ctx, accepted, at); !errors.Is(err, tasks.ErrConflict) {
		t.Fatal("stale Add accepted after dismissal", err)
	}
	other, _ := capture.SanitizeObservation("sh.blur.notes", "local", "Unrelated saved note", 4096)
	if err := s.SaveNote(ctx, other, at); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObservationRecord(ctx, source.ContentID()); err != nil {
		t.Fatal(err)
	}
	cleaned, found, err := s.GetTodaySnapshot(ctx, stale.Date)
	if err != nil || !found || len(cleaned.DismissedTodos) != 0 || len(cleaned.SourceIDs) != 1 || cleaned.SourceIDs[0] != other.ContentID() {
		t.Fatal("source erasure retained dismissal or removed unrelated memory", cleaned, found, err)
	}
	if err := s.PutTodaySnapshot(ctx, stale); err == nil {
		t.Fatal("late generation restored deleted evidence")
	}
}
