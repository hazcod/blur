package durable

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/power"
)

func TestRollupWeekPublicationFencedByHourReplacement(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xd4)
	at := time.Date(2018, 2, 6, 9, 0, 0, 0, time.UTC)
	source := putHistoricalHour(t, s, at)
	w := historicalRollupWorker(s)
	defer w.Close()
	for n := 0; n < 3; n++ {
		if err := w.ProcessRollups(ctx); err != nil {
			t.Fatal(err)
		}
	}
	prior, found, err := s.GetChronicleRollup(ctx, "week:2018-W06")
	if err != nil || !found || prior.Text == "" {
		t.Fatalf("week setup: %+v %v %v", prior, found, err)
	}
	hour, _, err := s.GetChronicle(ctx, "hour:20180206T09Z")
	if err != nil {
		t.Fatal(err)
	}
	hour.Text = "Corrected release decision."
	if err := s.PutChronicle(ctx, hour); err != nil {
		t.Fatal(err)
	}
	prior.Text = "- Stale release decision."
	if err := s.PutChronicleRollup(ctx, prior); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.GetChronicleRollup(ctx, prior.ID)
	if err != nil || !got.Dirty || got.Revision <= prior.Revision || got.Text == prior.Text {
		t.Fatalf("in-flight week survived changed hour before day generation: %+v %v (source %s)", got, err, source)
	}
}

func TestRollupZoneChangeRejectsOldGenerationBeforeBackfill(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xd5)
	at := time.Date(2018, 2, 6, 23, 0, 0, 0, time.UTC)
	putHistoricalHour(t, s, at)
	w := historicalRollupWorker(s)
	if err := w.ProcessRollups(ctx); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old, _, err := s.GetChronicleRollup(ctx, "day:2018-02-07")
	if err != nil || old.Text == "" {
		t.Fatalf("setup: %+v %v", old, err)
	}
	s.SetChronicleZone(func() string { return "America/Los_Angeles" })
	old.Text = "- Late old-zone generation."
	if err := s.PutChronicleRollup(ctx, old); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.GetChronicleRollup(ctx, old.ID)
	if err != nil || got.Text == old.Text {
		t.Fatalf("old zone generation was accepted: %+v %v", got, err)
	}
}

func TestRollupWorkCursorRotatesAndSurvivesDeletedCursor(t *testing.T) {
	ctx := context.Background()
	s, path, _ := rollupTestStore(t, 0xd6)
	for n := 1; n <= 90; n++ {
		at := time.Date(2018, 1, n, 0, 0, 0, 0, time.UTC)
		if err := s.MarkChronicleRollupDirty(ctx, "day:"+at.Format("2006-01-02"), rollupTestZone); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	var cursor string
	for n := 0; n < 64; n++ {
		row, found, err := s.NextChronicleRollupWork(ctx, "day", rollupTestZone)
		if err != nil || !found || seen[row.ID] {
			t.Fatalf("cursor stalled at%d: %+v %v %v", n, row, found, err)
		}
		seen[row.ID], cursor = true, row.ID
	}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.deleteRollup(cursor) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = OpenDurable(path, durableTestKey(t, 0xd6), OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for n := 0; n < 26; n++ {
		row, found, err := s.NextChronicleRollupWork(ctx, "day", rollupTestZone)
		if err != nil || !found || seen[row.ID] {
			t.Fatalf("restart repeated earlier work before tail%d: %+v %v %v", n, row, found, err)
		}
		seen[row.ID] = true
	}
	status, err := s.ChronicleRollupWorkStatus(ctx)
	if err != nil || status.Dirty != 89 {
		t.Fatalf("queue count after deletion: %+v %v", status, err)
	}
	if len(seen) != 90 {
		t.Fatal(fmt.Sprintf("visited%d of90", len(seen)))
	}
}

func historicalRollupWorker(store *Store) *chronicle.Worker {
	return chronicle.NewWorker(store, reviewFoundationPolicy{}, reviewFoundationModel{}, chronicle.WorkerConfig{
		Enabled: true, Provider: power.LocalProvider,
		Now:  func() time.Time { return time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC) },
		Zone: func() string { return rollupTestZone },
	})
}

func putHistoricalHour(t *testing.T, store *Store, at time.Time) string {
	t.Helper()
	source := rollupTestSource(t, store, "Reviewed the historical release evidence "+at.Format(time.RFC3339), at.Add(time.Minute))
	hour := chronicle.Summary{HourID: "hour:" + at.Format("20060102T15Z"), Start: at, End: at.Add(time.Hour), Text: "Reviewed historical release evidence.", SourceIDs: []string{source}}
	if err := store.PutChronicle(context.Background(), hour); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestHistoricalDirtyRollupsAreProcessedOutsideRecentHorizon(t *testing.T) {
	ctx := context.Background()
	store, _, _ := rollupTestStore(t, 0xd1)
	putHistoricalHour(t, store, time.Date(2018, 2, 6, 9, 0, 0, 0, time.UTC))
	worker := historicalRollupWorker(store)
	defer worker.Close()
	for n := 0; n < 4; n++ {
		if err := worker.ProcessRollups(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"day:2018-02-06", "week:2018-W06"} {
		row, found, err := store.GetChronicleRollup(ctx, id)
		if err != nil || !found || row.Dirty || row.Text == "" {
			t.Fatalf("historical work stranded: id=%s found=%v row=%+v err=%v", id, found, row, err)
		}
	}
}

func TestRollupWorkRecoversOldEpochAfterInterruptedErasureAndReopen(t *testing.T) {
	ctx := context.Background()
	store, path, _ := rollupTestStore(t, 0xd2)
	putHistoricalHour(t, store, time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC))
	before, _, err := store.GetChronicleRollup(ctx, "day:2026-11-15")
	if err != nil {
		t.Fatal(err)
	}
	// A process can stop immediately after the persistent erasure fence, before
	// its first row deletion. It must not leave retained inputs unwritable.
	if err := store.advanceRollupEpoch(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, durableTestKey(t, 0xd2), OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetChronicleZone(func() string { return rollupTestZone })
	worker := historicalRollupWorker(store)
	defer worker.Close()
	for n := 0; n < 4; n++ {
		if err := worker.ProcessRollups(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got, found, err := store.GetChronicleRollup(ctx, before.ID)
	if err != nil || !found || got.Dirty || got.Text == "" || got.Epoch <= before.Epoch {
		t.Fatalf("retained input stuck behind old epoch: found=%v row=%+v err=%v", found, got, err)
	}
	stale := got
	stale.Epoch = before.Epoch
	stale.Text = "- Stale pre-erasure content."
	if err := store.PutChronicleRollup(ctx, stale); err != nil {
		t.Fatal(err)
	}
	got, _, err = store.GetChronicleRollup(ctx, before.ID)
	if err != nil || got.Text == stale.Text {
		t.Fatalf("old epoch publication was accepted: %+v %v", got, err)
	}
}

func TestHistoricalBackfillResumesAcrossWorkerAndStoreRestart(t *testing.T) {
	ctx := context.Background()
	store, path, _ := rollupTestStore(t, 0xd3)
	for n := 0; n < 11; n++ {
		putHistoricalHour(t, store, time.Date(2015, 1, 2+n*33, 9, 0, 0, 0, time.UTC))
	}
	// Model an older schema-7 store with hourly recaps and no derived records.
	if err := store.DeleteAllChronicleRollups(ctx); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 8; pass++ {
		worker := historicalRollupWorker(store)
		if err := worker.ProcessRollups(ctx); err != nil {
			worker.Close()
			t.Fatal(err)
		}
		worker.Close()
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		var err error
		store, err = OpenDurable(path, durableTestKey(t, 0xd3), OpenExisting)
		if err != nil {
			t.Fatal(err)
		}
		store.SetChronicleZone(func() string { return rollupTestZone })
	}
	defer store.Close()
	for n := 0; n < 11; n++ {
		at := time.Date(2015, 1, 2+n*33, 9, 0, 0, 0, time.UTC)
		id := "day:" + at.Format("2006-01-02")
		row, found, err := store.GetChronicleRollup(ctx, id)
		if err != nil || !found || row.Dirty || row.Text == "" {
			t.Fatalf("restart skipped historical input: id=%s found=%v row=%+v err=%v", id, found, row, err)
		}
	}
}
