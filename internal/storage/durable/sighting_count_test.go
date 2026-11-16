package durable

import (
	"context"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
)

func TestSightingCountRoundTripAndIncrementsAcrossHours(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x61), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o, _ := capture.SanitizeObservation("editor.app", "example.com", "Mira owns the rollback and the schedule.", 512)
	first := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	if err := s.PutObservationAt(ctx, o, first); err != nil {
		t.Fatal(err)
	}
	count, last, found, err := s.ObservationSightingStats(ctx, o.ContentID())
	if err != nil || !found || count != 1 || !last.Equal(first) {
		t.Fatalf("fresh capture stats = %d %s %t %v", count, last, found, err)
	}
	for i, at := range []time.Time{first.Add(10 * time.Minute), first.Add(11 * time.Minute), first.Add(3 * time.Hour), first.Add(26 * time.Hour)} {
		recordTestSighting(t, s, o, at)
		count, last, found, err = s.ObservationSightingStats(ctx, o.ContentID())
		if err != nil || !found || count != int64(i+2) || !last.Equal(at) {
			t.Fatalf("after sighting %d: %d %s %t %v", i, count, last, found, err)
		}
	}
	// Re-recording the same instant is idempotent; an older instant counts
	// but never moves last-seen backwards.
	recordTestSighting(t, s, o, first.Add(26*time.Hour))
	recordTestSighting(t, s, o, first.Add(time.Hour))
	count, last, _, _ = s.ObservationSightingStats(ctx, o.ContentID())
	if count != 6 || !last.Equal(first.Add(26*time.Hour)) {
		t.Fatalf("monotonic stats = %d %s", count, last)
	}
}

func TestPreChangeSourceReadsCountOneAndUpgradesOnNextSighting(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x62), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o, _ := capture.SanitizeObservation("editor.app", "example.com", "Legacy capture without any count record at all.", 512)
	first := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	if err := s.PutObservationAt(ctx, o, first); err != nil {
		t.Fatal(err)
	}
	recordTestSighting(t, s, o, first.Add(time.Hour))
	// Simulate a record written by the previous release: no count entry.
	if err := s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return tx.deleteIndexValueLocked(observationSightingNS, sightingCountKey(o.ContentID()))
	}); err != nil {
		t.Fatal(err)
	}
	count, last, found, err := s.ObservationSightingStats(ctx, o.ContentID())
	if err != nil || !found || count != 1 || !last.Equal(first) {
		t.Fatalf("legacy decode = %d %s %t %v", count, last, found, err)
	}
	recordTestSighting(t, s, o, first.Add(2*time.Hour))
	count, last, _, err = s.ObservationSightingStats(ctx, o.ContentID())
	if err != nil || count != 2 || !last.Equal(first.Add(2*time.Hour)) {
		t.Fatalf("upgraded = %d %s %v", count, last, err)
	}
}

func TestSightingStatsUnknownSourceAndErasure(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x63), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o, _ := capture.SanitizeObservation("editor.app", "example.com", "A source that will be erased with its sightings.", 512)
	if _, _, found, err := s.ObservationSightingStats(ctx, o.ContentID()); err != nil || found {
		t.Fatalf("unknown source: %t %v", found, err)
	}
	first := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	if err := s.PutObservationAt(ctx, o, first); err != nil {
		t.Fatal(err)
	}
	recordTestSighting(t, s, o, first.Add(time.Hour))
	recordTestSighting(t, s, o, first.Add(5*time.Hour))
	if err := s.DeleteObservationRecord(ctx, o.ContentID()); err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := s.ObservationSightingStats(ctx, o.ContentID()); err != nil || found {
		t.Fatalf("erased source still reports stats: %t %v", found, err)
	}
	if err := s.PutObservationAt(ctx, o, first.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if count, _, _, err := s.ObservationSightingStats(ctx, o.ContentID()); err != nil || count != 1 {
		t.Fatalf("count leaked across erasure: %d %v", count, err)
	}
}
