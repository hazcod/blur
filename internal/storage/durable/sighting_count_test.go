package durable

import (
	"context"
	"errors"
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

func dropSightingCountRecord(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.Update(context.Background(), func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return tx.deleteIndexValueLocked(observationSightingNS, sightingCountKey(id))
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLegacySourceDerivesCountAndLastSeenFromChainThenContinues(t *testing.T) {
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
	// Capture plus two sightings in different hours, then strip the count
	// record to mimic a store written before counts existed.
	recordTestSighting(t, s, o, first.Add(10*time.Minute))
	recordTestSighting(t, s, o, first.Add(3*time.Hour))
	dropSightingCountRecord(t, s, o.ContentID())
	count, last, found, err := s.ObservationSightingStats(ctx, o.ContentID())
	if err != nil || !found || count != 3 || !last.Equal(first.Add(3*time.Hour)) {
		t.Fatalf("legacy derive = %d %s %t %v", count, last, found, err)
	}
	recordTestSighting(t, s, o, first.Add(5*time.Hour))
	count, last, _, err = s.ObservationSightingStats(ctx, o.ContentID())
	if err != nil || count != 4 || !last.Equal(first.Add(5*time.Hour)) {
		t.Fatalf("continued = %d %s %v", count, last, err)
	}
	// A legacy source with no sightings at all reports one (its capture).
	p, _ := capture.SanitizeObservation("editor.app", "example.com", "Never sighted again after the first capture.", 512)
	if err := s.PutObservationAt(ctx, p, first); err != nil {
		t.Fatal(err)
	}
	if count, last, _, _ = s.ObservationSightingStats(ctx, p.ContentID()); count != 1 || !last.Equal(first) {
		t.Fatalf("unsighted = %d %s", count, last)
	}
}

func TestCorruptCountRecordFailsReadAsCorruptAndRepairsOnNextSighting(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x64), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o, _ := capture.SanitizeObservation("editor.app", "example.com", "Capture whose count record will be damaged.", 512)
	first := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	if err := s.PutObservationAt(ctx, o, first); err != nil {
		t.Fatal(err)
	}
	recordTestSighting(t, s, o, first.Add(time.Hour))
	if err := s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return tx.putIndexValueLocked(observationSightingNS, sightingCountKey(o.ContentID()), []byte("{not json"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.ObservationSightingStats(ctx, o.ContentID()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("damaged record must read as corrupt, got %v", err)
	}
	repairs := SightingRepairs()
	recordTestSighting(t, s, o, first.Add(2*time.Hour))
	if SightingRepairs() != repairs+1 {
		t.Fatal("repair not counted")
	}
	count, last, _, err := s.ObservationSightingStats(ctx, o.ContentID())
	if err != nil || count != 3 || !last.Equal(first.Add(2*time.Hour)) {
		t.Fatalf("repaired = %d %s %v", count, last, err)
	}
}

func TestLegacyDerivationIsPersistedSoSecondReadWalksNoChain(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x65), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o, _ := capture.SanitizeObservation("editor.app", "example.com", "Legacy capture read twice by the memory tools.", 512)
	first := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	if err := s.PutObservationAt(ctx, o, first); err != nil {
		t.Fatal(err)
	}
	recordTestSighting(t, s, o, first.Add(time.Hour))
	recordTestSighting(t, s, o, first.Add(2*time.Hour))
	dropSightingCountRecord(t, s, o.ContentID())
	before := sightingChainWalks.Load()
	if count, _, _, err := s.ObservationSightingStats(ctx, o.ContentID()); err != nil || count != 3 {
		t.Fatalf("first read = %d %v", count, err)
	}
	walks := sightingChainWalks.Load()
	if walks == before {
		t.Fatal("first read should derive from the chain")
	}
	for i := 0; i < 3; i++ {
		if count, _, _, err := s.ObservationSightingStats(ctx, o.ContentID()); err != nil || count != 3 {
			t.Fatalf("later read = %d %v", count, err)
		}
	}
	if sightingChainWalks.Load() != walks {
		t.Fatalf("later reads walked the chain %d times", sightingChainWalks.Load()-walks)
	}
}

func TestOverlongSightingChainReportsUnknownAndPersistsNothing(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x66), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o, _ := capture.SanitizeObservation("editor.app", "example.com", "Capture with a sighting chain beyond the cap.", 512)
	first := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	if err := s.PutObservationAt(ctx, o, first); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		recordTestSighting(t, s, o, first.Add(time.Duration(i)*time.Hour))
	}
	dropSightingCountRecord(t, s, o.ContentID())
	old := maxSightingChainHops
	maxSightingChainHops = 2
	defer func() { maxSightingChainHops = old }()
	if _, _, _, err := s.ObservationSightingStats(ctx, o.ContentID()); !errors.Is(err, ErrSightingUnknown) {
		t.Fatalf("over cap must be unknown, got %v", err)
	}
	// A further sighting must not persist an undercount either.
	recordTestSighting(t, s, o, first.Add(9*time.Hour))
	if _, _, _, err := s.ObservationSightingStats(ctx, o.ContentID()); !errors.Is(err, ErrSightingUnknown) {
		t.Fatalf("over cap must stay unknown after a sighting, got %v", err)
	}
	var has bool
	if err := s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		var rerr error
		_, has, rerr = tx.readIndexValueLocked(observationSightingNS, sightingCountKey(o.ContentID()), 256)
		return rerr
	}); err != nil || has {
		t.Fatalf("a count record was persisted for an over-cap chain: %t %v", has, err)
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
