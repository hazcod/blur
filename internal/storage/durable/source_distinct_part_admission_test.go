package durable

import (
	"context"
	"fmt"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"sort"
	"testing"
	"time"
)

func TestReviewSixtyFourDistinctHistoricalCaptureAdmissions(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xe6)
	at := time.Date(2026, 11, 15, 9, 10, 0, 0, time.UTC)
	var ids []string
	for i := 0; i < 64; i++ {
		var edition string
		for _, when := range []time.Time{at, at.Add(24 * time.Hour)} {
			b, err := capture.NewCaptureBuilder("com.example.reader", "example.test", fmt.Sprintf("%064x", i+1), when)
			if err != nil {
				t.Fatal(err)
			}
			var parts []capture.CapturePart
			sink := func(_ context.Context, p []capture.CapturePart) error { parts = append(parts, p...); return nil }
			if err := b.Write(ctx, fmt.Sprintf("Historical source number%d confirms reviewed evidence.", i), sink); err != nil {
				t.Fatal(err)
			}
			summary, err := b.Finish(ctx, capture.CaptureComplete, sink)
			if err != nil {
				t.Fatal(err)
			}
			stageStoredCapture(t, s, b.Header(), parts)
			if _, err := s.PublishCapture(ctx, summary); err != nil {
				t.Fatal(err)
			}
			edition = summary.ContentID()
			b.Abort()
		}
		drainCaptureIndex(t, s)
		partIDs, _, err := s.ListCaptureSourceIDs(ctx, edition, -1, 64)
		if err != nil || len(partIDs) != 1 {
			t.Fatalf("fixture part IDs%d %v", len(partIDs), err)
		}
		ids = append(ids, partIDs[0])
	}
	sort.Strings(ids)
	start := at.Truncate(time.Hour)
	row := chronicle.Summary{HourID: "hour:" + start.Format("20060102T15Z"), Start: start, End: start.Add(time.Hour), Text: "Reviewed source evidence.", SourceIDs: ids}
	if err := s.PutChronicle(ctx, row); err != nil {
		t.Fatalf("valid64distinct historical part sources exceed metadata admission transaction: %v", err)
	}
}

func TestSixtyFourDistinctCaptureSourcesAtFractionalYearBoundary(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xe6)
	s.SetChronicleZone(func() string { return "Asia/Kathmandu" })
	at := time.Date(2023, 12, 31, 18, 10, 0, 0, time.UTC)
	var ids []string
	for i := 0; i < 64; i++ {
		var edition string
		for _, when := range []time.Time{at, at.Add(24 * time.Hour)} {
			b, err := capture.NewCaptureBuilder("com.example.reader", "example.test", fmt.Sprintf("%064x", i+1), when)
			if err != nil {
				t.Fatal(err)
			}
			var parts []capture.CapturePart
			sink := func(_ context.Context, p []capture.CapturePart) error { parts = append(parts, p...); return nil }
			if err := b.Write(ctx, fmt.Sprintf("Historical source number%d confirms reviewed evidence.", i), sink); err != nil {
				t.Fatal(err)
			}
			summary, err := b.Finish(ctx, capture.CaptureComplete, sink)
			if err != nil {
				t.Fatal(err)
			}
			stageStoredCapture(t, s, b.Header(), parts)
			if _, err := s.PublishCapture(ctx, summary); err != nil {
				t.Fatal(err)
			}
			edition = summary.ContentID()
			b.Abort()
		}
		drainCaptureIndex(t, s)
		partIDs, _, err := s.ListCaptureSourceIDs(ctx, edition, -1, 64)
		if err != nil || len(partIDs) != 1 {
			t.Fatalf("fixture part IDs%d %v", len(partIDs), err)
		}
		ids = append(ids, partIDs[0])
	}
	sort.Strings(ids)
	start := at.Truncate(time.Hour)
	row := chronicle.Summary{HourID: "hour:" + start.Format("20060102T15Z"), Start: start, End: start.Add(time.Hour), Text: "Reviewed source evidence.", SourceIDs: ids}
	// Revisited sources already have reverse links before the boundary hour is
	// backfilled; both insertion paths must fit the same physical budget.
	later := row
	later.Start, later.End = row.Start.Add(24*time.Hour), row.End.Add(24*time.Hour)
	later.HourID = "hour:" + later.Start.Format("20060102T15Z")
	if err := s.PutChronicle(ctx, later); err != nil {
		t.Fatalf("initial revisited-source hour: %v", err)
	}
	if err := s.PutChronicle(ctx, row); err != nil {
		t.Fatalf("valid64distinct historical part sources exceed metadata admission transaction: %v", err)
	}
}
