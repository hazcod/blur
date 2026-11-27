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

func TestLegacy64ToDistinctCapture64NewYearFitsBudget(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xe6)
	at := time.Date(2018, 2, 6, 9, 10, 0, 0, time.UTC)
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
		t.Fatal(err)
	}
	var old []string
	for n := 0; n < 64; n++ {
		old = append(old, rollupTestSource(t, s, fmt.Sprintf("Old source number%d", n), at))
	}
	sort.Strings(old)
	legacy := rollupTestRecord(t, "day:2018-02-06", "- Legacy64 supported the old recap.", []string{row.HourID}, old)
	installLegacyRollupForTest(t, s, legacy)
	for offset := 0; offset < len(ids); offset += 16 {
		if err := s.Update(ctx, func(tx *Tx) error {
			for _, source := range ids[offset:min(offset+16, len(ids))] {
				d := make([]byte, rollupSourceDirectoryBytes)
				d[2001/8] |= byte(1 << uint(2001%8))
				if err := tx.putRollupSourceDirectory(source, d); err != nil {
					return err
				}
				if err := tx.PutIndexRecord(rollupSourceYearNS, rollupSourceYearKey(source, 2001), mustJSON([]string{})); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	current, found, err := s.GetChronicleRollup(ctx, legacy.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	candidate := rollupTestRecord(t, legacy.ID, "- New64 capture subjects replace legacy64.", []string{row.HourID}, ids)
	candidate.Epoch = current.Epoch
	candidate.Revision = current.Revision
	if err := s.PutChronicleRollup(ctx, candidate); err != nil {
		t.Fatalf("legacy64 to new-year64 distinct capture replacement exceeds budget: %v", err)
	}
	got, found, err := s.GetChronicleRollup(ctx, legacy.ID)
	if err != nil || !found || got.Text != candidate.Text {
		t.Fatalf("replacement failed: found%v row%+v err%v", found, got, err)
	}
}

func TestDenseRollupSourceDirectoryEmptyYearSweepsRemainBounded(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xe7)
	at := time.Date(2018, 2, 6, 9, 0, 0, 0, time.UTC)
	source := rollupTestSource(t, s, "Historical source following512emptyyear shards.", at)
	r := rollupTestRecord(t, "day:9998-06-02", "- Live tail evidence.", []string{"hour:99980602T09Z"}, []string{source})
	if err := s.PutChronicleRollup(ctx, r); err != nil {
		t.Fatal(err)
	}
	d := make([]byte, rollupSourceDirectoryBytes)
	d[9998/8] |= byte(1 << uint(9998%8))
	for offset := 0; offset < 512; offset += 64 {
		if err := s.Update(ctx, func(tx *Tx) error {
			for n := offset; n < offset+64; n++ {
				year := n * 8
				d[year/8] |= byte(1 << uint(year%8))
				if err := tx.PutIndexRecord(rollupSourceYearNS, rollupSourceYearKey(source, year), mustJSON([]string{})); err != nil {
					return err
				}
			}
			return tx.putRollupSourceDirectory(source, d)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		done, err := tx.removeRollupsForSourceBatch(source)
		if err != nil {
			return err
		}
		if done {
			t.Fatal("16empty visits falsely completed before tail evidence")
		}
		raw, found, err := tx.readIndexValue(rollupSourceYearNS, "directory:"+source, rollupSourceDirectoryBytes+1)
		if err != nil {
			return err
		}
		if !found || len(raw) != 1251 || raw[0] != 2 {
			t.Fatalf("fixture not dense: found%v len%d", found, len(raw))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObservationRecord(ctx, source); err != nil {
		t.Fatalf("dense multi-quantum empty sweep failed: %v", err)
	}
	got, found, err := s.GetChronicleRollup(ctx, r.ID)
	if err != nil || !found || got.Text != "" || len(got.SourceIDs) != 0 {
		t.Fatalf("tail evidence survived: %v %+v %v", found, got, err)
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		_, found, err := s.rollupSourceDirectory(tx.writer, source)
		if err != nil {
			return err
		}
		if found {
			t.Fatal("directory remained after complete source erasure")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
