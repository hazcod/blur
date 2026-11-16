package durable

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func TestRevisitedSourceParticipatesInTimeScopedExactSearch(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x8d), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o, _ := capture.SanitizeObservation("editor.app", "example.com", "Mira owns the rollback.", 512)
	first := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	visit := first.AddDate(0, 0, 4).Add(10 * time.Minute)
	if err := s.PutObservationAt(ctx, o, first); err != nil {
		t.Fatal(err)
	}
	recordTestSighting(t, s, o, visit)
	recordTestSighting(t, s, o, visit.Add(30*time.Minute))
	resolved, found, lookupErr := s.ObservationSightingAt(ctx, o.ContentID(), visit.Add(-time.Minute), visit.Add(time.Minute))
	if lookupErr != nil || !found || !resolved.Equal(visit) {
		t.Fatalf("source/month visit lookup: %s %t %v", resolved, found, lookupErr)
	}
	q := search.ExactQuery{Terms: []string{"mira"}, CapturedStart: visit.Add(-time.Minute), CapturedEnd: visit.Add(time.Minute), TopK: 5, MaxRows: 16, MaxBytes: search.MaxIndexBytes}
	page, err := s.SearchExact(ctx, q)
	if err != nil || len(page.Hits) != 1 || !page.Hits[0].CapturedAt.Equal(visit) {
		t.Fatalf("revisit excluded before exact ranking: %+v %v", page, err)
	}
	q.CapturedStart, q.CapturedEnd = visit.Add(time.Minute), visit.Add(2*time.Minute)
	page, err = s.SearchExact(ctx, q)
	if err != nil || len(page.Hits) != 0 {
		t.Fatalf("unseen exact window leaked source: %+v %v", page, err)
	}
}

func recordTestSighting(t *testing.T, s *Store, o capture.SanitizedObservation, at time.Time) {
	t.Helper()
	recorder, ok := any(s).(interface {
		RecordObservationSighting(context.Context, capture.SanitizedObservation, time.Time) error
	})
	if !ok {
		t.Fatal("the encrypted store does not retain repeated sightings")
	}
	if err := recorder.RecordObservationSighting(context.Background(), o, at); err != nil {
		t.Fatal(err)
	}
}

func TestMonthOfRepeatedVisitsRemainsReadableWithOneUniqueSource(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x8e), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	first := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	o, _ := capture.SanitizeObservation("reader.app", "example.com", "The same page read throughout the month.", 512)
	if err := s.PutObservationAt(ctx, o, first); err != nil {
		t.Fatal(err)
	}
	for hour := 1; hour <= 17; hour++ {
		recordTestSighting(t, s, o, first.Add(time.Duration(hour)*time.Hour))
	}
	page, err := s.ListObservationWindow(ctx, storage.ObservationWindowRequest{Start: first, End: first.Add(31 * 24 * time.Hour), Limit: 5, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 1 || !page.CapturedAt[0].Equal(first.Add(17*time.Hour)) {
		t.Fatalf("ordinary repeated month lost its sole source: %+v %v", page, err)
	}
}

func TestRepeatedSightingsPreserveExactWindowsAndOriginalCapture(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0x8a)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	o, _ := capture.SanitizeObservation("editor.app", "example.com", "The rollback owner is Mira.", 512)
	first := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	today := first.AddDate(0, 0, 2)
	if err := s.PutObservationAt(ctx, o, first); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{today.Add(40 * time.Minute), today.Add(10 * time.Minute), today.Add(40 * time.Minute), first.Add(20 * time.Minute)} {
		recordTestSighting(t, s, o, at)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ start, end, want time.Time }{
		{first, first.Add(time.Minute), first},
		{first.Add(time.Minute), first.Add(time.Hour), first.Add(20 * time.Minute)},
		{today, today.Add(30 * time.Minute), today.Add(10 * time.Minute)},
		{today.Add(30 * time.Minute), today.Add(time.Hour), today.Add(40 * time.Minute)},
	} {
		page, err := s.ListObservationWindow(ctx, storage.ObservationWindowRequest{Start: tc.start, End: tc.end, Limit: 5, MaxBytes: storage.MaxObservationPageBytes})
		if err != nil || len(page.Observations) != 1 || !page.CapturedAt[0].Equal(tc.want) || page.Observations[0].Text() != o.Text() {
			t.Fatalf("visit window %s–%s: times=%v rows=%d err=%v, want %s", tc.start, tc.end, page.CapturedAt, len(page.Observations), err, tc.want)
		}
	}
	page, err := s.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: 5, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 1 || !page.CapturedAt[0].Equal(first) {
		t.Fatalf("visits changed primary text or first-capture provenance: %+v %v", page, err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{o.Text(), o.ContentID(), "example.com", "2026-11-11", "sighting"} {
		if bytes.Contains(disk, []byte(value)) {
			t.Fatalf("visit metadata leaked %q", value)
		}
	}
}

func TestRepeatedSightingPaginationReturnsEachSourceOnce(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x8b), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	start := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	a, _ := capture.SanitizeObservation("editor.app", "", "Visit Alpha", 512)
	b, _ := capture.SanitizeObservation("editor.app", "", "Visit Beta", 512)
	c, _ := capture.SanitizeObservation("editor.app", "", "Visit Gamma", 512)
	for i, o := range []capture.SanitizedObservation{a, b, c} {
		if err := s.PutObservationAt(ctx, o, start.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	recordTestSighting(t, s, a, start.Add(4*time.Hour))
	recordTestSighting(t, s, b, start.Add(3*time.Hour))
	request := storage.ObservationWindowRequest{Start: start, End: start.Add(5 * time.Hour), Limit: 1, MaxBytes: storage.MaxObservationPageBytes}
	for i, want := range []string{a.ContentID(), b.ContentID(), c.ContentID()} {
		page, err := s.ListObservationWindow(ctx, request)
		if err != nil || len(page.Observations) != 1 || page.Observations[0].ContentID() != want || page.HasMore != (i < 2) {
			t.Fatalf("page %d: %+v err=%v", i, page, err)
		}
		request.BeforeCapturedAt, request.BeforeSourceID = page.NextCapturedAt, page.NextSourceID
	}
}

func TestDeletedSourceRejectsLateSighting(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x8c), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o, _ := capture.SanitizeObservation("editor.app", "", "Deleted visit", 512)
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	if err := s.PutObservationAt(ctx, o, at); err != nil {
		t.Fatal(err)
	}
	recordTestSighting(t, s, o, at.Add(24*time.Hour))
	if err := s.DeleteObservationRecord(ctx, o.ContentID()); err != nil {
		t.Fatal(err)
	}
	recorder := any(s).(interface {
		RecordObservationSighting(context.Context, capture.SanitizedObservation, time.Time) error
	})
	if err := recorder.RecordObservationSighting(ctx, o, at.Add(48*time.Hour)); err == nil {
		t.Fatal("deleted source was admitted as a late visit")
	}
	page, err := s.ListObservationWindow(ctx, storage.ObservationWindowRequest{Start: at, End: at.Add(72 * time.Hour), Limit: 5, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 0 {
		t.Fatalf("deleted visit survived: %+v %v", page, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := recorder.RecordObservationSighting(canceled, o, at); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled visit: %v", err)
	}
}

func TestVisitHistoryErasureResumesAfterAnHourWasRemoved(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0x8f)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	o, _ := capture.SanitizeObservation("reader.app", "example.com", "Long-lived visited page", 512)
	first := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	if err := s.PutObservationAt(ctx, o, first); err != nil {
		t.Fatal(err)
	}
	const hours = 100
	for i := 0; i < hours; i++ {
		recordTestSighting(t, s, o, first.Add(time.Duration(i)*time.Hour+time.Minute))
	}
	interrupted := false
	err = s.deleteSourceStaged(ctx, o.ContentID(), func() error {
		journal, found, err := s.readSourceErasureLocked(ctx)
		if err != nil || !found {
			t.Fatal(found, err)
		}
		if journal.Phase != 7 {
			return nil
		}
		var remains bool
		err = s.state.db.View(ctx, func(reader bolt.Reader) error {
			_, remains, err = s.readIndexValueReader(reader, observationSightingNS, sightingHourKey(o.ContentID(), observationTimelineHourID(first.Add((hours-1)*time.Hour))), maxSightingHourBytes)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !remains {
			interrupted = true
			return context.Canceled
		}
		return nil
	})
	if !interrupted || !errors.Is(err, context.Canceled) {
		t.Fatal("did not interrupt an applied visit-erasure step", err)
	}
	if _, _, err := s.ObservationSightingAt(ctx, o.ContentID(), first, first.Add(24*time.Hour)); !errors.Is(err, ErrDeletionPending) {
		t.Fatal("partial erasure became readable", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetObservation(ctx, o.ContentID()); err != nil || found {
		t.Fatal("erasure recovery retained source", found, err)
	}
	for i := 0; i < hours; i++ {
		at := first.Add(time.Duration(i) * time.Hour)
		for _, id := range []string{o.ContentID(), sightingHourKey(o.ContentID(), observationTimelineHourID(at)), sightingMonthKey(o.ContentID(), at)} {
			_, found, err := s.readIndexValue(ctx, observationSightingNS, id, maxSightingHourBytes)
			if err != nil || found {
				t.Fatalf("visit metadata survived erasure: %q %t %v", id, found, err)
			}
		}
	}
}

func TestPublicDeletionDrainsMoreThanOneVisitTransaction(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x90), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o, _ := capture.SanitizeObservation("reader.app", "", "Public deletion with many visits", 512)
	first := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if err := s.PutObservationAt(ctx, o, first); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		recordTestSighting(t, s, o, first.Add(time.Duration(i)*time.Hour+time.Minute))
	}
	if err := s.DeleteObservationRecord(ctx, o.ContentID()); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListObservationWindow(ctx, storage.ObservationWindowRequest{Start: first, End: first.Add(48 * time.Hour), Limit: 5, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 0 {
		t.Fatal("public deletion retained visit rows", page, err)
	}
}

func TestRepeatedQuickNoteRetainsItsLaterDateForRecall(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x92), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o, _ := capture.SanitizeObservation("sh.blur.notes", "local", "Mira owns the rollback", 512)
	first := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	later := first.AddDate(0, 0, 2)
	if err := s.SaveNote(ctx, o, first); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveNote(ctx, o, later); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListObservationWindow(ctx, storage.ObservationWindowRequest{Start: later, End: later.Add(time.Hour), Limit: 5, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 1 || !page.CapturedAt[0].Equal(later) {
		t.Fatalf("repeated note missing its later date: %+v %v", page, err)
	}
}
