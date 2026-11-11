package durable

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/today"
)

func TestDeleteObservationRecordRemovesItsTodayProjection(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x74), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	generated := time.Date(2026, 11, 5, 17, 0, 0, 0, time.UTC)
	source, err := capture.SanitizeObservation("editor.app", "docs.example", "sensitive note", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservation(ctx, source); err != nil {
		t.Fatal(err)
	}
	snapshot, err := today.Build(today.Input{Date: "2026-11-05", Zone: "UTC", GeneratedAt: generated, GenerationID: "g-1", Evidence: []today.Evidence{{SourceID: source.ContentID(), At: generated, Change: "sensitive note"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTodaySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteObservationRecord(ctx, source.ContentID()); err != nil {
		t.Fatal(err)
	}
	if got, found, err := store.GetTodaySnapshot(ctx, snapshot.Date); err != nil || found {
		t.Fatalf("deleted source projection still exists: snapshot=%+v found=%t err=%v", got, found, err)
	}
}

func TestDeleteSourceCleansEveryIndexedTodayDateAndPreservesUnrelatedEvidence(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x75), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sourceID, otherID := repeatHex("a"), repeatHex("b")
	firstAt := time.Date(2026, 11, 5, 17, 0, 0, 0, time.UTC)
	first, err := today.Build(today.Input{Date: "2026-11-05", Zone: "UTC", GeneratedAt: firstAt, GenerationID: "g-1", Evidence: []today.Evidence{{SourceID: sourceID, At: firstAt, Change: "private first day"}}})
	if err != nil {
		t.Fatal(err)
	}
	secondAt := firstAt.Add(24 * time.Hour)
	second, err := today.Build(today.Input{Date: "2026-11-06", Zone: "UTC", GeneratedAt: secondAt, GenerationID: "g-2", Evidence: []today.Evidence{
		{SourceID: sourceID, At: secondAt.Add(-time.Minute), Change: "private deleted content"},
		{SourceID: otherID, At: secondAt, Change: "unrelated retained content"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range []today.Snapshot{first, second} {
		if err := store.PutTodaySnapshot(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteObservationRecord(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetTodaySnapshot(ctx, first.Date); err != nil || found {
		t.Fatalf("single-source date remains: found=%t err=%v", found, err)
	}
	got, found, err := store.GetTodaySnapshot(ctx, second.Date)
	if err != nil || !found || len(got.SourceIDs) != 1 || got.SourceIDs[0] != otherID || len(got.RecentChanges) != 1 || got.RecentChanges[0].Text != "unrelated retained content" {
		t.Fatalf("source removal damaged or leaked adjacent evidence: %+v found=%t err=%v", got, found, err)
	}
}

func TestDeleteObservationTransactionRollbackRestoresTodayProjection(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x76), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 11, 5, 17, 0, 0, 0, time.UTC)
	source, err := capture.SanitizeObservation("editor.app", "docs.example", "rollback source", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservation(ctx, source); err != nil {
		t.Fatal(err)
	}
	snapshot, err := today.Build(today.Input{Date: "2026-11-05", Zone: "UTC", GeneratedAt: at, GenerationID: "g-1", Evidence: []today.Evidence{{SourceID: source.ContentID(), At: at, Change: "must survive rollback"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTodaySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	err = store.Update(ctx, func(tx *Tx) error {
		if err := tx.deleteObservationRecord(source.ContentID()); err != nil {
			return err
		}
		return errors.New("force rollback")
	})
	if !errors.Is(err, ErrTransaction) {
		t.Fatalf("rollback error = %v", err)
	}
	got, found, err := store.GetTodaySnapshot(ctx, snapshot.Date)
	if err != nil || !found || len(got.RecentChanges) != 1 || got.RecentChanges[0].Text != "must survive rollback" {
		t.Fatalf("today projection changed after rollback: snapshot=%+v found=%t err=%v", got, found, err)
	}
}

func TestTodaySnapshotIsEncryptedReplaceableAndReopenable(t *testing.T) {
	ctx := context.Background()
	path := durableTestPath(t)
	key := durableTestKey(t, 0x72)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	generated := time.Date(2026, 11, 5, 17, 0, 0, 0, time.UTC)
	snapshot, err := today.Build(today.Input{Date: "2026-11-05", Zone: "UTC", GeneratedAt: generated, GenerationID: "g-1", Evidence: []today.Evidence{{SourceID: "source-secret", At: generated.Add(-time.Hour), SubjectID: "project-secret", Subject: "Project secret", Change: "Changed private"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTodaySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	replacement, err := today.Build(today.Input{Date: "2026-11-05", Zone: "UTC", GeneratedAt: generated.Add(time.Minute), GenerationID: "g-2", Evidence: []today.Evidence{{SourceID: "source-next", At: generated, SubjectID: "project-next", Subject: "Next"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTodaySnapshot(ctx, replacement); err != nil {
		t.Fatalf("replace snapshot atomically: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	physical, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{[]byte("source-next"), []byte("project-next"), []byte("Next"), []byte("g-2")} {
		if bytes.Contains(physical, secret) {
			t.Fatalf("today plaintext persisted: %q", secret)
		}
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, found, err := store.GetTodaySnapshot(ctx, "2026-11-05")
	if err != nil || !found {
		t.Fatalf("GetTodaySnapshot() found=%v err=%v", found, err)
	}
	if got.GenerationID != "g-2" || !today.Equal(got, replacement) {
		t.Fatalf("reopened snapshot %#v", got)
	}
	got.ActiveSubjects[0].Label = "mutated"
	again, found, err := store.GetTodaySnapshot(ctx, "2026-11-05")
	if err != nil || !found || again.ActiveSubjects[0].Label != "Next" {
		t.Fatal("read result mutated persisted snapshot")
	}
}

func TestTodaySnapshotRejectsInvalidDateAndHonorsContext(t *testing.T) {
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x73), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.GetTodaySnapshot(context.Background(), "../secret"); err == nil {
		t.Fatal("invalid date accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.GetTodaySnapshot(ctx, "2026-11-05"); err == nil {
		t.Fatal("canceled read accepted")
	}
}
