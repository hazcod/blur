package durable

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/securestore"
)

func openReplicaTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestExportObservationsOrdersByCaptureTimeAndHonorsWatermark(t *testing.T) {
	ctx := context.Background()
	store := openReplicaTestStore(t)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	for i := 4; i >= 0; i-- { // insert out of order
		o, err := capture.SanitizeObservation("editor", "", fmt.Sprintf("entry %d", i), 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, o, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	items, more, err := store.ExportObservations(ctx, time.Time{}, 3, 1<<20)
	if err != nil || len(items) != 3 || !more {
		t.Fatalf("first page = %d, more=%v, %v", len(items), more, err)
	}
	for i, item := range items {
		if !item.CapturedAt.Equal(base.Add(time.Duration(i) * time.Minute)) {
			t.Fatalf("item %d at %v", i, item.CapturedAt)
		}
		if _, err := capture.RestoreAuthenticatedObservation(item.Record); err != nil {
			t.Fatal(err)
		}
	}
	rest, more, err := store.ExportObservations(ctx, items[2].CapturedAt, 10, 1<<20)
	if err != nil || len(rest) != 2 || more {
		t.Fatalf("rest = %d, more=%v, %v", len(rest), more, err)
	}
	if _, _, err := store.ExportObservations(ctx, time.Time{}, 0, 1); err == nil {
		t.Fatal("zero count accepted")
	}
}

func TestTombstoneLogIsBoundedSortedAndDeduplicated(t *testing.T) {
	ctx := context.Background()
	store := openReplicaTestStore(t)
	id := fmt.Sprintf("%064x", 7)
	at := time.Now().UTC().Truncate(time.Microsecond)
	for _, tomb := range []Tombstone{{id, at}, {id, at}, {AllSourcesTombstone, at.Add(time.Second)}} {
		if err := store.RecordTombstone(ctx, tomb); err != nil {
			t.Fatal(err)
		}
	}
	log, err := store.Tombstones(ctx)
	if err != nil || len(log) != 2 || log[0].SourceID != id || log[1].SourceID != AllSourcesTombstone {
		t.Fatalf("log = %+v, %v", log, err)
	}
	if !TombstoneCovers(log, id, at.Add(-time.Minute)) || TombstoneCovers(log, id, at.Add(time.Minute)) {
		t.Fatal("source tombstone coverage is wrong")
	}
	if !TombstoneCovers(log, fmt.Sprintf("%064x", 9), at) || TombstoneCovers(log, fmt.Sprintf("%064x", 9), at.Add(time.Minute)) {
		t.Fatal("all-before tombstone coverage is wrong")
	}
	if err := store.RecordTombstone(ctx, Tombstone{SourceID: "bad", At: at}); err == nil {
		t.Fatal("invalid tombstone accepted")
	}
}

func TestImportedMarksRoundTripAndClear(t *testing.T) {
	ctx := context.Background()
	store := openReplicaTestStore(t)
	a, b, c := fmt.Sprintf("%064x", 1), fmt.Sprintf("%064x", 2), "ff"+fmt.Sprintf("%062x", 3)
	for i := 0; i < 2; i++ { // idempotent
		if err := store.MarkImported(ctx, []string{a, c}); err != nil {
			t.Fatal(err)
		}
	}
	set, err := store.ImportedSet(ctx, []string{a, b, c})
	if err != nil || !set[a] || set[b] || !set[c] {
		t.Fatalf("set = %v, %v", set, err)
	}
	if err := store.ClearImported(ctx); err != nil {
		t.Fatal(err)
	}
	if set, err = store.ImportedSet(ctx, []string{a, c}); err != nil || len(set) != 0 {
		t.Fatalf("after clear = %v, %v", set, err)
	}
	if err := store.MarkImported(ctx, []string{"bad"}); err == nil {
		t.Fatal("invalid ID accepted")
	}
}
