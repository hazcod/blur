package durable

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func resumeRecapErasureForTest(t *testing.T, s *Store) {
	t.Helper()
	for n := 0; n < 100; n++ {
		done := false
		if err := s.Update(context.Background(), func(tx *Tx) error { var err error; done, err = tx.resumeChronicleErasure(); return err }); err != nil {
			t.Fatal(err)
		}
		if done {
			return
		}
	}
	t.Fatal("bounded erasure did not finish")
}

func TestRecapErasureJournalFencesDuringEpochRevisionZeroPublication(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xc9)
	at := time.Date(2018, 2, 6, 9, 0, 0, 0, time.UTC)
	source := putHistoricalHour(t, s, at)
	if err := s.beginChronicleErasure(ctx, false); err != nil {
		t.Fatal(err)
	}
	var duringEpoch uint64
	if err := s.Update(ctx, func(tx *Tx) error { var err error; duringEpoch, err = s.rollupEpochReader(tx.writer); return err }); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, func(tx *Tx) error { _, err := tx.resumeChronicleErasure(); return err }); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkChronicleRollupDirty(ctx, "day:2018-02-06", rollupTestZone); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.NextChronicleRollupWork(ctx, "day", rollupTestZone); err != nil || found {
		t.Fatalf("new work claimed during erasure: %v %v", found, err)
	}
	candidate := rollupTestRecord(t, "day:2018-02-06", "- Late current-epoch generation.", []string{"hour:20180206T09Z"}, []string{source})
	candidate.Epoch = duringEpoch // a claim from this epoch must not outlive the erase
	if err := s.PutChronicleRollup(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		_, found, err := tx.readRollup(candidate.ID)
		if err != nil {
			return err
		}
		if found {
			t.Fatal("gate hid a row that was actually republished during erasure")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	resumeRecapErasureForTest(t, s)
	if err := s.PutChronicleRollup(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if row, found, err := s.GetChronicleRollup(ctx, candidate.ID); err != nil || found {
		t.Fatalf("Revision0 candidate resurrected erased period: %+v %v %v", row, found, err)
	}
	if _, found, err := s.GetChronicle(ctx, "hour:20180206T09Z"); err != nil || !found {
		t.Fatalf("rollups-only erasure removed retained hour: %v %v", found, err)
	}
}

func TestInterruptedRecapErasureSuppressesReadsAndResumesAcrossReopen(t *testing.T) {
	for _, hours := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollups", true: "hours and rollups"}[hours], func(t *testing.T) {
			ctx := context.Background()
			s, path, _ := rollupTestStore(t, 0xca)
			var sources []string
			for day := 6; day <= 8; day++ {
				sources = append(sources, putHistoricalHour(t, s, time.Date(2018, 2, day, 9, 0, 0, 0, time.UTC)))
			}
			w := historicalRollupWorker(s)
			for n := 0; n < 3; n++ {
				if err := w.ProcessRollups(ctx); err != nil {
					t.Fatal(err)
				}
			}
			w.Close()
			if err := s.beginChronicleErasure(ctx, hours); err != nil {
				t.Fatal(err)
			}
			// Crash after intent committed but before its first erase quantum.
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			s, err = OpenDurable(path, durableTestKey(t, 0xca), OpenExisting)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.SetChronicleZone(func() string { return rollupTestZone })
			if _, found, err := s.GetChronicleRollup(ctx, "day:2018-02-06"); err != nil || found {
				t.Fatalf("pending derivative remained publicly readable: %v %v", found, err)
			}
			from := time.Date(2018, 2, 5, 0, 0, 0, 0, time.UTC)
			to := from.AddDate(0, 0, 5)
			if rows, next, err := s.ListChronicleRollups(ctx, "day", from, to, "", 64); err != nil || len(rows) != 0 || next != "" {
				t.Fatalf("pending rollup list leaked: %v %q %v", rows, next, err)
			}
			row, found, err := s.GetChronicle(ctx, "hour:20180206T09Z")
			if err != nil || found == hours {
				t.Fatalf("hour visibility for erase kind%v: %+v %v %v", hours, row, found, err)
			}
			if hours {
				if rows, next, err := s.ListChronicles(ctx, from, to, "", 64); err != nil || len(rows) != 0 || next != "" {
					t.Fatalf("pending hour list leaked: %v %q %v", rows, next, err)
				}
				if err := s.Update(ctx, func(tx *Tx) error {
					_, found, err := tx.readIndexValue(chronicleNS, "hour:20180206T09Z", 32<<10)
					if err != nil {
						return err
					}
					if !found {
						t.Fatal("fixture had no underlying retained hour to suppress")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				// New hourly evidence may arrive during rollup clearing. It must
				// survive and be rediscovered after the ending epoch fence.
				sources = append(sources, putHistoricalHour(t, s, time.Date(2018, 2, 9, 9, 0, 0, 0, time.UTC)))
			}
			w = historicalRollupWorker(s)
			defer w.Close()
			for pass := 0; pass < 30; pass++ {
				if err := w.ProcessRollups(ctx); err != nil {
					t.Fatal(err)
				}
				status, err := s.ChronicleRollupWorkStatus(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !status.ErasurePending && !status.BackfillPending && status.Dirty == 0 {
					break
				}
				if pass == 29 {
					t.Fatalf("interrupted erasure stranded work: %+v", status)
				}
			}
			for n, source := range sources {
				if _, found, err := s.GetSourceEvidence(ctx, source); err != nil || !found {
					t.Fatalf("recap erasure changed original: %v %v", found, err)
				}
				day := time.Date(2018, 2, 6+n, 9, 0, 0, 0, time.UTC)
				_, found, err := s.GetChronicle(ctx, "hour:"+day.Format("20060102T15Z"))
				if err != nil || found == hours {
					t.Fatalf("hour after recovery%v: %v %v", hours, found, err)
				}
				r, found, err := s.GetChronicleRollup(ctx, "day:"+day.Format("2006-01-02"))
				if err != nil || found == hours || found && (r.Text == "" || r.Dirty || !slices.Equal(r.SourceIDs, []string{source})) {
					t.Fatalf("day after recovery%v: %+v %v %v", hours, r, found, err)
				}
			}
		})
	}
}

func TestAllRecapErasureBlocksHourPublicationAndEscalatesRollupIntent(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xcb)
	putHistoricalHour(t, s, time.Date(2018, 2, 6, 9, 0, 0, 0, time.UTC))
	hour, _, err := s.GetChronicle(ctx, "hour:20180206T09Z")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.beginChronicleErasure(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := s.beginChronicleErasure(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := s.PutChronicle(ctx, hour); !errors.Is(err, errChronicleErasurePending) {
		t.Fatalf("hour publication bypassed all-recap gate: %v", err)
	}
	resumeRecapErasureForTest(t, s)
	if _, found, err := s.GetChronicle(ctx, hour.HourID); err != nil || found {
		t.Fatalf("escalated erase kept hour: %v %v", found, err)
	}
}

func TestRecapErasureMaintenanceResumesWithoutDiscoveryOrInference(t *testing.T) {
	ctx := context.Background()
	s, path, _ := rollupTestStore(t, 0xcc)
	source := putHistoricalHour(t, s, time.Date(2018, 2, 6, 9, 0, 0, 0, time.UTC))
	if err := s.beginChronicleErasure(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = OpenDurable(path, durableTestKey(t, 0xcc), OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for pass := 0; pass < 12; pass++ {
		worked, more, err := s.ResumeChronicleErasure(ctx)
		if err != nil || !worked {
			t.Fatalf("committed erasure did not advance: %v %v", worked, err)
		}
		if !more {
			break
		}
		if pass == 11 {
			t.Fatal("model-free erasure did not finish")
		}
	}
	if _, found, err := s.GetChronicle(ctx, "hour:20180206T09Z"); err != nil || found {
		t.Fatalf("erased hour remained: %v %v", found, err)
	}
	if _, found, err := s.GetSourceEvidence(ctx, source); err != nil || !found {
		t.Fatalf("recap maintenance changed original evidence: %v %v", found, err)
	}
	for n := 0; n < 2; n++ {
		if worked, more, err := s.ResumeChronicleErasure(ctx); err != nil || worked || more {
			t.Fatalf("idle maintenance created new work: %v %v %v", worked, more, err)
		}
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		state, err := s.rollupBackfillReader(tx.writer)
		if err == nil && state.Version != 0 {
			t.Fatalf("model-free erasure started historical backfill: %+v", state)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
