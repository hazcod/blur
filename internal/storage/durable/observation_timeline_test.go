package durable

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
)

func TestObservationWindowFindsEveryCaptureBeyondChronicleBudget(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xa1), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	start := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	const count = 96
	want := make([]string, count)
	for i := count - 1; i >= 0; i-- {
		observation, err := capture.SanitizeObservation("editor.app", "", fmt.Sprintf("Timeline note number %03d", i), 512)
		if err != nil {
			t.Fatal(err)
		}
		want[i] = observation.ContentID()
		if err := store.PutObservationAt(ctx, observation, start.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	request := storage.ObservationWindowRequest{Start: start, End: start.Add(time.Hour), Limit: 17, MaxBytes: storage.MaxObservationPageBytes}
	var got []string
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		page, err := store.ListObservationWindow(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		for i, observation := range page.Observations {
			index := count - 1 - len(got)
			if index < 0 || observation.ContentID() != want[index] || !page.CapturedAt[i].Equal(start.Add(time.Duration(index)*time.Second)) {
				t.Fatalf("capture %d not in newest-first capture order", len(got))
			}
			got = append(got, observation.ContentID())
		}
		if !page.HasMore {
			break
		}
		if len(page.Observations) == 0 || page.NextSourceID == request.BeforeSourceID {
			t.Fatal("window continuation did not advance")
		}
		request.BeforeCapturedAt, request.BeforeSourceID = page.NextCapturedAt, page.NextSourceID
	}
	if len(got) != count {
		t.Fatalf("time index returned %d captures, want all %d (chronicle limits must not hide primary evidence)", len(got), count)
	}
	if err := store.DeleteObservationRecord(ctx, want[count-1]); err != nil {
		t.Fatal(err)
	}
	request.BeforeCapturedAt, request.BeforeSourceID = time.Time{}, ""
	page, err := store.ListObservationWindow(ctx, request)
	if err != nil || len(page.Observations) != request.Limit || page.Observations[0].ContentID() != want[count-2] {
		t.Fatalf("delete did not update time index: page=%+v err=%v", page, err)
	}
}

func TestSourceDeletionFailsAtomicallyWhenTimelineProvenanceIsMissing(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xa6), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	observation, _ := capture.SanitizeObservation("editor.app", "", "A source whose deletion must remain atomic", 512)
	if err := store.PutObservation(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, func(tx *Tx) error {
		return tx.deleteKnowledgeRecord(observationTimelineSourceNS, observation.ContentID())
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteObservationRecord(ctx, observation.ContentID()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("missing deletion provenance returned %v, want corruption error", err)
	}
	if _, found, err := store.GetObservation(ctx, observation.ContentID()); err != nil || !found {
		t.Fatalf("failed deletion removed the primary source: found=%v err=%v", found, err)
	}
}

func TestObservationTimelineResumesBackfillOnReopen(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xa2)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	var ids []string
	for index := 0; index < 6; index++ {
		observation, err := capture.SanitizeObservation("editor.app", "", fmt.Sprintf("Legacy memory %d", index), 512)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, observation, start.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, observation.ContentID())
	}
	first, err := store.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: 1, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(first.Observations) != 1 {
		t.Fatalf("migration cursor fixture: %+v err=%v", first, err)
	}
	cursor := first.Observations[0].ContentID()
	// Simulate an interrupted migration: its first source and encrypted cursor
	// were committed; all other sources still lack timeline entries.
	if err := store.Update(ctx, func(tx *Tx) error {
		for _, id := range ids {
			if id != cursor {
				if err := tx.removeObservationTimelineSource(id); err != nil {
					return err
				}
			}
		}
		return tx.PutIndexRecord(captureTimeMigrationNamespace, observationTimelineMarkerID, append([]byte{1, 0}, []byte(cursor)...))
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ListObservationWindow(ctx, storage.ObservationWindowRequest{Start: start, End: start.Add(time.Hour), Limit: 8, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != len(ids) {
		t.Fatalf("resumed timeline = %d captures, err=%v; want %d", len(page.Observations), err, len(ids))
	}
	for index, at := range page.CapturedAt {
		if !at.Equal(start.Add(time.Duration(5-index) * time.Minute)) {
			t.Fatalf("backfill changed original capture time: %s", at)
		}
	}
	marker, found, err := store.readIndexValue(ctx, captureTimeMigrationNamespace, observationTimelineMarkerID, 66)
	if err != nil || !found || len(marker) != 2 || marker[1] != 1 {
		t.Fatalf("migration not marked complete: %v, %v, %v", marker, found, err)
	}
	clear(marker)
	for _, id := range ids {
		if err := store.DeleteObservationRecord(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	page, err = store.ListObservationWindow(ctx, storage.ObservationWindowRequest{Start: start, End: start.Add(time.Hour), Limit: 8, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 0 || page.HasMore {
		t.Fatalf("deleted sources remained reachable: %+v err=%v", page, err)
	}
}

func TestObservationWindowFiltersBeforeSelectingRecentPrefix(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xa3), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	start := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	selected, _ := capture.SanitizeObservation("com.kagi.kagimacos", "research.example", "The page to remember", 512)
	if err := store.PutObservationAt(ctx, selected, start); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 70; index++ {
		other, _ := capture.SanitizeObservation("other.app", "other.example", fmt.Sprintf("Unrelated item %d", index), 512)
		if err := store.PutObservationAt(ctx, other, start.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	for _, filter := range []struct {
		app, domain string
		exclude     []string
	}{{app: "com.kagi.kagimacos"}, {domain: "research.example"}, {app: "other.app", domain: "research.example"}, {exclude: []string{"other.app"}}} {
		page, err := store.ListObservationWindow(ctx, storage.ObservationWindowRequest{Start: start, End: start.Add(time.Hour), AppID: filter.app, Domain: filter.domain, ExcludeAppIDs: filter.exclude, Limit: 1, MaxBytes: storage.MaxObservationPageBytes})
		if err != nil {
			t.Fatal(err)
		}
		if filter.app == "other.app" {
			if len(page.Observations) != 0 {
				t.Fatal("app and domain filters must both match")
			}
		} else if len(page.Observations) != 1 || page.Observations[0].ContentID() != selected.ContentID() || page.HasMore {
			t.Fatalf("filtered time query selected an unrelated recent prefix: %+v", page)
		}
	}
}
