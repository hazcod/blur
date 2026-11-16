package durable

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/tasks"
	"github.com/hazcod/blur/internal/today"
)

func TestTodayConfirmationCreatesOneDurableTaskWithoutScheduling(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xe3)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	source, _ := capture.SanitizeObservation("sh.blur.notes", "local", "Review Atlas tomorrow", 4096)
	if err := s.SaveNote(ctx, source, at); err != nil {
		t.Fatal(err)
	}
	proposal := notes.Proposal{ID: "review-atlas", Kind: notes.Reminder, Text: "Review Atlas", Due: "tomorrow", SourceID: source.ContentID(), CreatedAt: at}
	if err := s.SaveProposals(ctx, source.ContentID(), []notes.Proposal{proposal}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmProposal(ctx, "2026-11-12", proposal.ID); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListTasks(ctx, tasks.PageRequest{Limit: 32})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("Today confirmation did not create a real task", page, err)
	}
	item := page.Items[0]
	if item.SourceID != source.ContentID() || item.UserAuthored || item.State != tasks.Open || item.Reminder != nil || item.Delivery.State != "" {
		t.Fatal("suggested timing became unauthorized scheduling, or provenance was lost", item)
	}
	snapshot, _, err := s.GetTodaySnapshot(ctx, "2026-11-12")
	if err != nil || len(snapshot.TodoCandidates) != 0 || len(snapshot.ConfirmedTodos) != 1 || snapshot.ConfirmedTodos[0].TaskID != item.ID {
		t.Fatal("task and confirmation link were not committed together", snapshot, err)
	}
	item.Title, item.State, item.Revision = "Review the Atlas release", tasks.Completed, item.Revision+1
	item.UpdatedAt = item.UpdatedAt.Add(time.Minute)
	if err := s.PutTask(ctx, item, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProposals(ctx, source.ContentID(), []notes.Proposal{proposal}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmProposal(ctx, "2026-11-12", proposal.ID); err != nil {
		t.Fatal("confirmation retry was not idempotent", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	page, err = s.ListTasks(ctx, tasks.PageRequest{Limit: 32})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != item.ID || page.Items[0].Title != "Review the Atlas release" || page.Items[0].State != tasks.Completed || page.Items[0].Revision != 2 {
		t.Fatal("retry/reopen replaced an edited task", page, err)
	}
	snapshot, _, err = s.GetTodaySnapshot(ctx, "2026-11-12")
	if err != nil || len(snapshot.TodoCandidates) != 0 {
		t.Fatal("reprocessing resurrected a confirmed suggestion", snapshot, err)
	}
	if err := s.DeleteTask(ctx, item.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmProposal(ctx, "2026-11-12", proposal.ID); !errors.Is(err, tasks.ErrConflict) {
		t.Fatal("a deleted task was automatically recreated", err)
	}
	if err := s.DeleteObservationRecord(ctx, source.ContentID()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetTodaySnapshot(ctx, "2026-11-12"); err != nil || found {
		t.Fatal("source deletion left a Today task projection", found, err)
	}
}

func TestTodayTaskReceiptSurvivesAStaleSnapshotWrite(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe4), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	source, _ := capture.SanitizeObservation("sh.blur.notes", "local", "TODO: Review Atlas", 4096)
	if err := s.SaveNote(ctx, source, at); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProposals(ctx, source.ContentID(), []notes.Proposal{{ID: "atlas", Kind: notes.Task, Text: "Review Atlas", SourceID: source.ContentID(), CreatedAt: at}}); err != nil {
		t.Fatal(err)
	}
	stale, _, _ := s.GetTodaySnapshot(ctx, "2026-11-12")
	if err := s.ConfirmProposal(ctx, stale.Date, "atlas"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTodaySnapshot(ctx, stale); err != nil {
		t.Fatal(err)
	}
	current, _, err := s.GetTodaySnapshot(ctx, stale.Date)
	if err != nil || len(current.TodoCandidates) != 0 || len(current.ConfirmedTodos) != 1 || !tasks.ValidID(current.ConfirmedTodos[0].TaskID) {
		t.Fatal("an activity/note refresh undid explicit task confirmation", current, err)
	}
}

func TestTodayTaskRejectsChangedPreviewAndMissingSourceAtomically(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe5), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	var ids []string
	for _, text := range []string{"Atlas review request", "Atlas review context"} {
		source, _ := capture.SanitizeObservation("com.example.chat", "local", text, 4096)
		if err := s.PutObservationAt(ctx, source, at); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, source.ContentID())
	}
	sort.Strings(ids)
	snapshot := today.Snapshot{Date: "2026-11-12", Zone: "UTC", GeneratedAt: at, GenerationID: "fixture", SourceIDs: ids, TodoCandidates: []today.Todo{{ID: "review", Text: "Review Atlas", SourceIDs: ids}}}
	if err := s.PutTodaySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	request := tasks.TodayCandidate{Date: snapshot.Date, ID: "review", Text: "Review Atlas", SourceIDs: ids}
	changed := request
	changed.Text = "Review something else"
	if _, err := s.ConfirmTodayTask(ctx, changed, at); !errors.Is(err, tasks.ErrConflict) {
		t.Fatal("unseen text became a task", err)
	}
	if err := s.DeleteObservationRecord(ctx, ids[1]); err != nil {
		t.Fatal(err)
	}
	// An authenticated legacy orphan may predate the current live-source
	// guard. The confirmation path must still reject that old projection.
	if err := s.Update(ctx, func(tx *Tx) error { return tx.PutIndexRecord(todayNamespace, snapshot.Date, mustJSON(snapshot)) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmTodayTask(ctx, request, at); !errors.Is(err, tasks.ErrSourceGone) {
		t.Fatal("a removed source authorized a task", err)
	}
	page, err := s.ListTasks(ctx, tasks.PageRequest{Limit: 32})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("a failed confirmation partly saved a task", page, err)
	}
	current, _, err := s.GetTodaySnapshot(ctx, snapshot.Date)
	if err != nil || len(current.ConfirmedTodos) != 0 || len(current.TodoCandidates) != 1 {
		t.Fatal("failed confirmation changed the review state", current, err)
	}
}

func TestTodayTaskCapacityFailureKeepsTheSuggestionUnconfirmed(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe7), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	source, _ := capture.SanitizeObservation("sh.blur.notes", "local", "TODO: Review Atlas", 4096)
	if err := s.SaveNote(ctx, source, at); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProposals(ctx, source.ContentID(), []notes.Proposal{{ID: "atlas", Kind: notes.Task, Text: "Review Atlas", SourceID: source.ContentID(), CreatedAt: at}}); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= tasks.MaxSourceTasks; n++ {
		item := taskFixture(n)
		item.UserAuthored, item.SourceID = false, source.ContentID()
		if err := s.PutTask(ctx, item, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ConfirmProposal(ctx, "2026-11-12", "atlas"); !errors.Is(err, tasks.ErrCapacity) {
		t.Fatal("source/task capacity failure was hidden", err)
	}
	snapshot, _, err := s.GetTodaySnapshot(ctx, "2026-11-12")
	if err != nil || len(snapshot.ConfirmedTodos) != 0 || len(snapshot.TodoCandidates) != 1 || snapshot.TodoCandidates[0].TaskID != "" {
		t.Fatal("failed task creation nevertheless confirmed the suggestion", snapshot, err)
	}
}
