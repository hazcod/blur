package durable

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/nielsgroen/blur/internal/today"
)

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
