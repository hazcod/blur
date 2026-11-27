package durable

import (
	"context"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/today"
)

// A Finder-launched process can call its location "Local" while Today already
// uses the machine's IANA zone. Rejecting those names loses an ordinary save.
func TestUserNoteSavesIntoCanonicalTodayWithLocalAlias(t *testing.T) {
	for _, tc := range []struct {
		zone, instant, date string
		offset              int
	}{
		{"Europe/Brussels", "2026-11-22T01:20:00Z", "2026-11-22", 3600},
		{"Europe/Brussels", "2026-10-25T23:30:00Z", "2026-10-26", 3600},
		{"Asia/Kathmandu", "2026-11-21T20:00:00Z", "2026-11-22", 20700},
	} {
		t.Run(tc.zone+tc.date, func(t *testing.T) {
			t.Setenv("TZ", tc.zone)
			previousLocal := time.Local
			time.Local = time.FixedZone("Local", tc.offset)
			t.Cleanup(func() { time.Local = previousLocal })
			ctx := context.Background()
			s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x72), CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			at, err := time.Parse(time.RFC3339, tc.instant)
			if err != nil {
				t.Fatal(err)
			}
			prior, err := capture.SanitizeObservation("com.apple.TextEdit", "fixture.local", "Prior saved context for the daily recap.", 1024)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.PutObservationAt(ctx, prior, at.Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			snapshot, err := today.Build(today.Input{Date: tc.date, Zone: tc.zone, GeneratedAt: at.Add(-time.Minute), GenerationID: "canonical-existing-day", Evidence: []today.Evidence{{SourceID: prior.ContentID(), At: at.Add(-time.Minute), Change: "Prior saved context"}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.PutTodaySnapshot(ctx, snapshot); err != nil {
				t.Fatal(err)
			}
			const text = "Blur acceptance note — Copper Harbor review is Monday. Reference amber 9136."
			result, err := notes.NewService(s, nil, func() time.Time { return at }).AddReceipt(ctx, text)
			if err != nil {
				t.Fatalf("basic note save failed beside canonical Today zone %s: %v", tc.zone, err)
			}
			original, found, err := s.GetObservation(ctx, result.SourceID)
			if err != nil || !found || original.Text() != text {
				t.Fatalf("durable note missing after save: found=%t err=%v", found, err)
			}
			got, found, err := s.GetTodaySnapshot(ctx, tc.date)
			if err != nil || !found || got.Zone != tc.zone || len(got.SourceIDs) != 2 || !got.GeneratedAt.Equal(at) {
				t.Fatalf("note changed calendar provenance or lost source: %+v %t %v", got, found, err)
			}
		})
	}
}

func TestReviewLegacyLocalTodayAllowsNewNote(t *testing.T) {
	t.Setenv("TZ", "Europe/Brussels")
	old := time.Local
	time.Local = time.FixedZone("Local", 3600)
	t.Cleanup(func() { time.Local = old })
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x73), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	source, err := capture.SanitizeObservation("sh.blur.notes", "local", "Existing note saved by the prior build.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(ctx, source, at.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := today.Build(today.Input{Date: "2026-11-22", Zone: "Local", GeneratedAt: at.Add(-time.Minute), GenerationID: "legacy-local-note", Evidence: []today.Evidence{{SourceID: source.ContentID(), At: at.Add(-time.Minute), Change: "Existing note"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutTodaySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := notes.NewService(s, nil, func() time.Time { return at }).AddReceipt(ctx, "A new ordinary note after upgrading the application."); err != nil {
		t.Fatalf("legacy Local snapshot blocks note save: %v", err)
	}
}
