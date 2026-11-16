package durable

import (
	"context"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage"
)

// Reproduce the authenticated state left by an older capture-time migration,
// including an already-generated summary. Original dates cannot be recovered.
func migratedTimeFixture(t *testing.T) (*Store, capture.SanitizedObservation, time.Time) {
	t.Helper()
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xc9)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	baseline := time.Date(2026, 11, 12, 12, 30, 0, 123, time.UTC)
	o, _ := capture.SanitizeObservation("reader.app", "example.com", "Aurora legacy memory", 512)
	if err := s.PutObservationAt(ctx, o, baseline); err != nil {
		t.Fatal(err)
	}
	hour := baseline.Truncate(time.Hour)
	if err := s.PutChronicle(ctx, chronicle.Summary{HourID: observationTimelineHourID(hour), Start: hour, End: hour.Add(time.Hour), Text: "Legacy Aurora work", SourceIDs: []string{o.ContentID()}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		return tx.PutIndexRecord(captureTimeMigrationNamespace, captureTimeMigrationID, encodeCaptureTimeMigration(baseline, true))
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, o, baseline
}

func TestMigrationBaselineDoesNotProveDatedActivity(t *testing.T) {
	for _, kind := range []string{"window", "exact", "sighting", "hour", "summary read", "summary list", "summary write"} {
		t.Run(kind, func(t *testing.T) {
			s, o, baseline := migratedTimeFixture(t)
			ctx := context.Background()
			start, end := baseline.Truncate(time.Hour), baseline.Truncate(time.Hour).Add(time.Hour)
			switch kind {
			case "window":
				page, err := s.ListObservationWindow(ctx, storage.ObservationWindowRequest{Start: start, End: end, Limit: 8, MaxBytes: storage.MaxObservationPageBytes})
				if err != nil || len(page.Observations) != 0 {
					t.Fatalf("upgrade date became observed activity: %+v %v", page, err)
				}
			case "exact":
				page, err := s.SearchExact(ctx, search.ExactQuery{Terms: []string{"aurora"}, CapturedStart: start, CapturedEnd: end, TopK: 8, MaxRows: 8, MaxBytes: search.MaxIndexBytes})
				if err != nil || len(page.Hits) != 0 {
					t.Fatalf("dated exact query admitted upgrade baseline: %+v %v", page, err)
				}
			case "sighting":
				at, found, err := s.ObservationSightingAt(ctx, o.ContentID(), start, end)
				if err != nil || found {
					t.Fatalf("upgrade baseline became a sighting: %v %t %v", at, found, err)
				}
			case "hour":
				hour, err := s.CaptureHourEvidence(ctx, start)
				if err != nil || len(hour.Evidence) != 0 {
					t.Fatalf("hourly recap received guessed capture date: %+v %v", hour, err)
				}
			case "summary read":
				_, found, err := s.GetChronicle(ctx, observationTimelineHourID(start))
				if err != nil || found {
					t.Fatalf("legacy summary still asserts a guessed date: %t %v", found, err)
				}
			case "summary list":
				rows, _, err := s.ListChronicles(ctx, start, end, "", 8)
				if err != nil || len(rows) != 0 {
					t.Fatalf("dated recap list admitted guessed activity: %+v %v", rows, err)
				}
			case "summary write":
				err := s.PutChronicle(ctx, chronicle.Summary{HourID: observationTimelineHourID(start), Start: start, End: end, Text: "Incorrectly dated legacy activity", SourceIDs: []string{o.ContentID()}})
				if err == nil {
					t.Fatal("new summary accepted a retention baseline as observed evidence")
				}
			}
			// These reads must preserve both unscoped recall and retention age.
			page, err := s.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: 8, MaxBytes: storage.MaxObservationPageBytes})
			if err != nil || len(page.Observations) != 1 || page.Observations[0].Text() != "Aurora legacy memory" || !page.CapturedAt[0].Equal(baseline) {
				t.Fatalf("provenance handling changed original/retention metadata: %+v %v", page, err)
			}
			matches, err := s.SearchExact(ctx, search.ExactQuery{Terms: []string{"aurora"}, TopK: 8, MaxRows: 8, MaxBytes: search.MaxIndexBytes})
			if err != nil || len(matches.Hits) != 1 {
				t.Fatalf("legacy content disappeared from unscoped recall: %+v %v", matches, err)
			}
		})
	}
}

func TestRealVisitsRemainDatedAroundUnknownMigrationBaseline(t *testing.T) {
	s, o, baseline := migratedTimeFixture(t)
	ctx := context.Background()
	before, after := baseline.Add(-15*time.Minute), baseline.Add(15*time.Minute)
	for _, at := range []time.Time{before, after} {
		if err := s.RecordObservationSighting(ctx, o, at); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ start, end, want time.Time }{
		{baseline.Add(-time.Minute), baseline.Add(time.Minute), time.Time{}},
		{before.Add(-time.Minute), before.Add(time.Minute), before},
		{after.Add(-time.Minute), after.Add(time.Minute), after},
		{baseline.Truncate(time.Hour), baseline.Truncate(time.Hour).Add(time.Hour), after},
	} {
		page, err := s.ListObservationWindow(ctx, storage.ObservationWindowRequest{Start: tc.start, End: tc.end, Limit: 8, MaxBytes: storage.MaxObservationPageBytes})
		if err != nil {
			t.Fatal(err)
		}
		at, found, err := s.ObservationSightingAt(ctx, o.ContentID(), tc.start, tc.end)
		if tc.want.IsZero() {
			if err != nil || found || len(page.Observations) != 0 {
				t.Fatalf("seeded unknown baseline leaked into scoped visits: %+v %v %t %v", page, at, found, err)
			}
		} else if err != nil || !found || !at.Equal(tc.want) || len(page.Observations) != 1 || !page.CapturedAt[0].Equal(tc.want) {
			t.Fatalf("genuine sighting lost: %+v %v %t %v; want %v", page, at, found, err, tc.want)
		}
	}
}

func TestRealCaptureBesideMigrationBaselineKeepsItsDate(t *testing.T) {
	s, _, baseline := migratedTimeFixture(t)
	ctx := context.Background()
	o, _ := capture.SanitizeObservation("reader.app", "example.com", "Newly observed compiler work", 512)
	at := baseline.Add(time.Nanosecond)
	if err := s.PutObservationAt(ctx, o, at); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListObservationWindow(ctx, storage.ObservationWindowRequest{Start: baseline, End: baseline.Add(time.Minute), Limit: 8, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 1 || page.Observations[0].ContentID() != o.ContentID() || !page.CapturedAt[0].Equal(at) {
		t.Fatalf("provenance classifier suppressed genuine nearby capture: %+v %v", page, err)
	}
	start := baseline.Truncate(time.Hour)
	summary := chronicle.Summary{HourID: observationTimelineHourID(start), Start: start, End: start.Add(time.Hour), Text: "Genuine compiler work", SourceIDs: []string{o.ContentID()}}
	if err := s.PutChronicle(ctx, summary); err != nil {
		t.Fatal(err)
	}
	if got, found, err := s.GetChronicle(ctx, summary.HourID); err != nil || !found || got.Text != "Genuine compiler work" {
		t.Fatalf("genuine same-hour summary was hidden: %+v %t %v", got, found, err)
	}
}
