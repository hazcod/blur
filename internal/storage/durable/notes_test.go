package durable

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/today"
)

func TestUserNoteAndSourceLinkedProposalsPersistEncryptedInToday(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x79), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
	observation, err := capture.SanitizeObservation("sh.blur.notes", "local", "Follow up with Alex about the draft tomorrow", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNote(ctx, observation, at); err != nil {
		t.Fatal(err)
	}
	proposals := []notes.Proposal{
		{ID: "proposal-1", Kind: notes.Reminder, Text: "Follow up with Alex", Due: "2026-11-06", SourceID: observation.ContentID(), CreatedAt: at},
		{ID: "question-1", Kind: notes.Question, Text: "Who agreed to send the draft?", SourceID: observation.ContentID(), CreatedAt: at},
		{ID: "question-2", Kind: notes.Question, Text: "When will the next review happen?", SourceID: observation.ContentID(), CreatedAt: at},
	}
	if err := store.SaveProposals(ctx, observation.ContentID(), proposals); err != nil {
		t.Fatal(err)
	}
	snapshot, found, err := store.GetTodaySnapshot(ctx, "2026-11-05")
	if err != nil || !found {
		t.Fatalf("today snapshot found=%v err=%v", found, err)
	}
	if len(snapshot.RecentChanges) != 1 || snapshot.RecentChanges[0].SourceIDs[0] != observation.ContentID() || snapshot.RecentChanges[0].Origin != today.ChangeOriginUser || len(snapshot.TodoCandidates) != 1 || snapshot.TodoCandidates[0].SourceIDs[0] != observation.ContentID() || len(snapshot.Questions) != 2 || snapshot.Questions[0].SourceIDs[0] != observation.ContentID() {
		t.Fatalf("note/todo source link lost: %+v", snapshot)
	}
	if err := store.ConfirmProposal(ctx, "2026-11-05", proposals[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := store.AnswerQuestion(ctx, "2026-11-05", "question-1", "Alex will send it Friday", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	snapshot, found, err = store.GetTodaySnapshot(ctx, "2026-11-05")
	if err != nil || !found || !snapshot.Questions[0].Resolved || snapshot.Questions[0].Dismissed || len(snapshot.Questions[0].AnswerSourceID) != 64 {
		t.Fatalf("answer did not resolve and link question: found=%v err=%v snapshot=%+v", found, err, snapshot)
	}
	if err := store.DismissQuestion(ctx, "2026-11-05", "question-2"); err != nil {
		t.Fatal(err)
	}
	snapshot, found, err = store.GetTodaySnapshot(ctx, "2026-11-05")
	if err != nil || !found || !snapshot.Questions[1].Resolved || !snapshot.Questions[1].Dismissed {
		t.Fatalf("dismissal not persisted: found=%v err=%v snapshot=%+v", found, err, snapshot)
	}
	answer, answerFound, err := store.GetObservation(ctx, snapshot.Questions[0].AnswerSourceID)
	if err != nil || !answerFound || answer.Text() != "Alex will send it Friday" {
		t.Fatalf("answer note missing: found=%v err=%v", answerFound, err)
	}
	snapshot, found, err = store.GetTodaySnapshot(ctx, "2026-11-05")
	if err != nil || !found || len(snapshot.TodoCandidates) != 0 || len(snapshot.ConfirmedTodos) != 1 || !snapshot.ConfirmedTodos[0].Confirmed {
		t.Fatalf("confirmation failed: found=%v err=%v snapshot=%+v", found, err, snapshot)
	}
	stored, exists, err := store.GetObservation(ctx, observation.ContentID())
	if err != nil || !exists || stored.Text() != observation.Text() {
		t.Fatalf("note source exists=%v err=%v", exists, err)
	}
	if err := store.DeleteObservationRecord(ctx, observation.ContentID()); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := store.GetObservation(ctx, observation.ContentID()); err != nil || exists {
		t.Fatalf("deleted note source exists=%v err=%v", exists, err)
	}
	snapshot, found, err = store.GetTodaySnapshot(ctx, "2026-11-05")
	if err != nil || !found || len(snapshot.TodoCandidates) != 0 || len(snapshot.ConfirmedTodos) != 0 || len(snapshot.Questions) != 0 || len(snapshot.RecentChanges) != 1 || snapshot.RecentChanges[0].Text != "Alex will send it Friday" {
		t.Fatalf("deleting note failed to purge linked projections while preserving answer note: found=%v err=%v snapshot=%+v", found, err, snapshot)
	}
	if err := store.DeleteObservationRecord(ctx, snapshot.RecentChanges[0].SourceIDs[0]); err != nil {
		t.Fatal(err)
	}
	remaining, found, err := store.GetTodaySnapshot(ctx, "2026-11-05")
	if err != nil || found {
		t.Fatalf("deleting the answer note left source-linked Today data: found=%v err=%v snapshot=%+v", found, err, remaining)
	}
}

func TestTodayNoteOmittedWhenReverseSourceIndexIsFull(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x92), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
	snapshot := today.Snapshot{Date: "2026-11-05", Zone: "UTC", GeneratedAt: at, GenerationID: "full-index"}
	for i := 0; i < today.MaxSources; i++ {
		snapshot.SourceIDs = append(snapshot.SourceIDs, fmt.Sprintf("%064x", i+1))
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("full index fixture invalid: %v", err)
	}
	for end := 32; ; end += 32 {
		if end > len(snapshot.SourceIDs) {
			end = len(snapshot.SourceIDs)
		}
		partial := snapshot.Clone()
		partial.SourceIDs = partial.SourceIDs[:end]
		if err := store.PutTodaySnapshot(ctx, partial); err != nil {
			t.Fatal(err)
		}
		if end == len(snapshot.SourceIDs) {
			break
		}
	}
	// Sort order ensures this valid note source cannot displace an existing
	// reverse-index entry when the hard source ceiling is reached.
	noteSource := strings.Repeat("f", 64)
	got, err := store.noteSnapshot(ctx, snapshot.Date, snapshot.Zone, at.Add(time.Minute), noteSource, "A note outside the bounded recap index")
	if err != nil {
		t.Fatal(err)
	}
	if got.Validate() != nil || containsSource(got.SourceIDs, noteSource) || len(got.RecentChanges) != 0 || got.Completeness.ChangesOmitted != 1 {
		t.Fatalf("full source index emitted an unindexed Today change: %+v", got)
	}
	snapshot.Questions = []today.Question{{ID: "question-full", Text: "Who owns the plan?", SourceIDs: []string{snapshot.SourceIDs[0]}, CreatedAt: at}}
	if err := store.PutTodaySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	answer, err := capture.SanitizeObservation("sh.blur.notes", "local", "The owner is Alex", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AnswerQuestion(ctx, snapshot.Date, "question-full", answer.Text(), at.Add(time.Minute)); err == nil {
		t.Fatal("answered question when its source could not be indexed")
	}
	if _, found, err := store.GetObservation(ctx, answer.ContentID()); err != nil || found {
		t.Fatalf("rejected answer was partially persisted: found=%v err=%v", found, err)
	}
}

func containsSource(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
