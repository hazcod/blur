package durable

import (
	"context"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
)

func TestExactSearchRanksInsideCaptureWindowAndAllowsLaterSightings(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xa4), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	start := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	older, _ := capture.SanitizeObservation("deployment.app", "", "Older discussion", 512)
	recent, _ := capture.SanitizeObservation("editor.app", "", "The deployment plan", 512)
	if err := store.PutObservationAt(ctx, older, start.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, recent, start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	query := search.ExactQuery{Terms: []string{"deployment"}, TopK: 1, MaxRows: search.MaxIndexRows, MaxBytes: search.MaxIndexBytes, CapturedStart: start, CapturedEnd: start.AddDate(0, 0, 1)}
	page, err := store.SearchExact(ctx, query)
	if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != recent.ContentID() || !page.Hits[0].CapturedAt.Equal(start.Add(time.Hour)) {
		t.Fatalf("scoped ranking = %+v err=%v, want recent text match despite a stronger old app-name match", page, err)
	}
	query.ScopeSourceIDs = []string{older.ContentID()}
	page, err = store.SearchExact(ctx, query)
	if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != older.ContentID() {
		t.Fatalf("later-sighting source was excluded by its first capture time: %+v err=%v", page, err)
	}
}

func TestExactSearchExcludesOwnAppBeforeTopKSelection(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xa5), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	self, _ := capture.SanitizeObservation("com.hazcod.blur", "", "A captured search form", 512)
	pageSource, _ := capture.SanitizeObservation("editor.app", "", "Blur project notes", 512)
	for _, observation := range []capture.SanitizedObservation{self, pageSource} {
		if err := store.PutObservation(ctx, observation); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.SearchExact(ctx, search.ExactQuery{Terms: []string{"blur"}, ExcludeAppIDs: []string{"com.hazcod.blur"}, TopK: 1, MaxRows: search.MaxIndexRows, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != pageSource.ContentID() {
		t.Fatalf("self-capture exclusion = %+v err=%v, want the actual source", page, err)
	}
}
