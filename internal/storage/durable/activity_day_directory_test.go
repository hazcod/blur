package durable

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
)

func TestActivityDayDirectoryPreservesRowsBeyondLegacyLimitAndStableMiddleCursor(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xe7)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	source, err := capture.SanitizeObservation("reader.app", "", "Proof for intervals in the middle and final activity pages.", 512)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(ctx, source, at); err != nil {
		t.Fatal(err)
	}
	// Real encrypted records and the production day/reverse writers run in
	// small transactions, avoiding thousands of unnecessary fsyncs in a test.
	for first := 0; first < 4097; first += 16 {
		err := s.Update(ctx, func(tx *Tx) error {
			for i := first; i < min(first+16, 4097); i++ {
				start := at.Add(time.Duration(2*i) * time.Second)
				segment := activity.Segment{ID: fmt.Sprintf("busy-day-%05d", i), WallStart: start, WallEnd: start.Add(time.Second), Duration: int64(time.Second), AppID: "reader.app"}
				if i >= 2048 && i < 2112 || i == 4096 {
					segment.SourceIDs = []string{source.ContentID()}
					if _, err := tx.trackActivitySource(source.ContentID(), segment.ID); err != nil {
						return err
					}
				}
				if err := tx.addActivityDay(segment); err != nil {
					return err
				}
				if err := tx.PutIndexRecord(activityNamespace, segment.ID, mustJSON(segment)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("accepted day rows stopped at %d: %v", first, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	count, after := 0, ""
	for {
		rows, next, err := s.ListActivitySegmentsForDate(ctx, "2026-11-20", after, 64)
		if err != nil {
			t.Fatal("large day pagination failed", err)
		}
		count += len(rows)
		if next == "" {
			break
		}
		after = next
	}
	if count != 4097 {
		t.Fatalf("day pagination omitted accepted rows: %d", count)
	}
	if err := s.DeleteObservationRecord(ctx, source.ContentID()); err != nil {
		t.Fatal("source erase failed across middle and tail pages", err)
	}
	rows, _, err := s.ListActivitySegmentsForDate(ctx, "2026-11-20", "busy-day-02047", 1)
	if err != nil || len(rows) != 1 || rows[0].ID != "busy-day-02112" {
		t.Fatalf("middle deletion broke stable cursor: %+v %v", rows, err)
	}
	rows, next, err := s.ListActivitySegmentsForDate(ctx, "2026-11-20", "busy-day-04095", 1)
	if err != nil || len(rows) != 0 || next != "" {
		t.Fatalf("tail page deletion left invalid directory: %+v %s %v", rows, next, err)
	}
	start := at.Add(3 * time.Hour)
	if err := s.PutActivitySegment(ctx, activity.Segment{ID: "after-tail-erasure", WallStart: start, WallEnd: start.Add(time.Second), Duration: int64(time.Second), AppID: "reader.app"}); err != nil {
		t.Fatal("append after tail deletion failed", err)
	}
}

func TestActivityRangeReadsBeyondDefaultTransactionQuantum(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xeb), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	// This report needs more than the default 1,024 physical operations but
	// fits the backend's separate bounded read snapshot.
	const count = 1025
	for first := 0; first < count; first += 16 {
		if err := s.Update(ctx, func(tx *Tx) error {
			for i := first; i < min(first+16, count); i++ {
				start := at.Add(time.Duration(2*i) * time.Second)
				segment := activity.Segment{ID: fmt.Sprintf("report-row-%05d", i), WallStart: start, WallEnd: start.Add(time.Second), Duration: int64(time.Second), AppID: "reader.app"}
				if err := tx.addActivityDay(segment); err != nil {
					return err
				}
				if err := tx.PutIndexRecord(activityNamespace, segment.ID, mustJSON(segment)); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ListActivitySegmentsForDates(ctx, []string{"2026-11-20"}, count)
	if err != nil || len(rows) != count || rows[0].ID != "report-row-00000" || rows[count-1].ID != "report-row-01024" {
		t.Fatalf("extended bounded activity report omitted rows: count=%d err=%v", len(rows), err)
	}
}

func TestActivityDayLastRowDeletionDoesNotScanHistoricalPageHighWater(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe8), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	segment := activity.Segment{ID: "last-live-page", WallStart: at, WallEnd: at.Add(time.Second), Duration: int64(time.Second), AppID: "reader.app"}
	const pageID = "2026-11-20:p:999999"
	if err := s.Update(ctx, func(tx *Tx) error {
		for _, record := range []struct {
			ns, id string
			value  []byte
		}{
			{activityNamespace, segment.ID, mustJSON(segment)},
			{activityDayNamespace, "2026-11-20", []byte(`{"version":2,"pages":1000000,"count":1,"first":999999,"last":999999}`)},
			{activityDayNamespace, pageID, mustJSON([]string{segment.ID})},
			{"activity-day-links-v2", pageID, []byte(`{"before":-1,"after":-1}`)},
			{activityDayRefNamespace, segment.ID, []byte(pageID)},
		} {
			if err := tx.PutIndexRecord(record.ns, record.id, record.value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.removeActivityDay(segment) }); err != nil {
		t.Fatal("one live page required unbounded historical cleanup", err)
	}
	if rows, _, err := s.ListActivitySegmentsForDate(ctx, "2026-11-20", "", 1); err != nil || len(rows) != 0 {
		t.Fatalf("last day row remained reachable: %+v %v", rows, err)
	}
}

func TestActivityDayDirectoryMigratesLegacyPagesAndRetainsOldSegmentCursor(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xea), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	segment := activity.Segment{ID: "legacy-day-row", WallStart: at, WallEnd: at.Add(time.Second), Duration: int64(time.Second), AppID: "reader.app"}
	if err := s.Update(ctx, func(tx *Tx) error {
		for _, row := range []struct {
			ns, id string
			value  []byte
		}{
			{activityNamespace, segment.ID, mustJSON(segment)},
			{activityDayNamespace, "2026-11-20", []byte(`{"pages":2,"count":1}`)},
			{activityDayNamespace, "2026-11-20:p:00", []byte(`[]`)},
			{activityDayNamespace, "2026-11-20:p:01", mustJSON([]string{segment.ID})},
			{activityDayRefNamespace, segment.ID, []byte("2026-11-20:p:01")},
		} {
			if err := tx.PutIndexRecord(row.ns, row.id, row.value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	start := at.Add(2 * time.Second)
	if err := s.PutActivitySegment(ctx, activity.Segment{ID: "new-day-row", WallStart: start, WallEnd: start.Add(time.Second), Duration: int64(time.Second), AppID: "reader.app"}); err != nil {
		t.Fatal("legacy day metadata migration failed", err)
	}
	rows, next, err := s.ListActivitySegmentsForDate(ctx, "2026-11-20", "legacy-day-row", 1)
	if err != nil || len(rows) != 1 || rows[0].ID != "new-day-row" || next != "" {
		t.Fatalf("legacy cursor changed after directory migration: %+v %s %v", rows, next, err)
	}
}
