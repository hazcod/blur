package durable

import (
	"context"
	"fmt"
	"github.com/hazcod/blur/internal/chronicle"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// Write the old reverse representation as an older schema-7 build did. Calling
// the current source-link writer would not reproduce a legacy transition.
func installLegacyRollupForTest(t *testing.T, s *Store, r chronicle.Rollup) {
	t.Helper()
	if err := s.Update(context.Background(), func(tx *Tx) error {
		for _, source := range r.SourceIDs {
			ids, _, err := tx.readRollupSources(source)
			if err != nil {
				return err
			}
			i := sort.SearchStrings(ids, r.ID)
			if i < len(ids) && ids[i] == r.ID {
				continue
			}
			ids = slices.Insert(ids, i, r.ID)
			if err := tx.PutIndexRecord(chronicleRollupSourceNS, source, mustJSON(ids)); err != nil {
				return err
			}
		}
		if err := tx.addRollupShard(r.ID); err != nil {
			return err
		}
		return tx.writeRollup(r, &r)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestInputOnlyErasureClearsLegacyDayAndWeekWithinOneQuantum(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xc8)
	var kept, children []string
	var private string
	for batch, count := range []int{63, 64, 64, 64, 1} {
		at := time.Date(2018, 2, 6, 9+batch, 0, 0, 0, time.UTC)
		var sources []string
		for n := 0; n < count; n++ {
			id := rollupTestSource(t, s, fmt.Sprintf("Legacy retained capture batch%d item%d", batch, n), at.Add(time.Minute))
			sources = append(sources, id)
			kept = append(kept, id)
		}
		sort.Strings(sources)
		hour := chronicle.Summary{HourID: "hour:" + at.Format("20060102T15Z"), Start: at, End: at.Add(time.Hour), Text: "Reviewed project evidence.", SourceIDs: sources}
		if batch == 0 {
			private = rollupTestSource(t, s, "Private input-only ownership decision.", at.Add(2*time.Minute))
			hour.InputSourceIDs = append(slices.Clone(sources), private)
			sort.Strings(hour.InputSourceIDs)
			hour.InputReceipt = strings.Repeat("a", 64)
		}
		if err := s.PutChronicle(ctx, hour); err != nil {
			t.Fatal(err)
		}
		children = append(children, hour.HourID)
	}
	sort.Strings(kept)
	day := rollupTestRecord(t, "day:2018-02-06", "- The private decision influenced the work.", children, kept)
	week := rollupTestRecord(t, "week:2018-W06", "- The private decision influenced the week.", []string{day.ID}, kept)
	month := rollupTestRecord(t, "month:2018-02", "- The private decision influenced the month.", []string{day.ID}, kept)
	year := rollupTestRecord(t, "year:2018", "- The private decision influenced the year.", []string{month.ID}, kept)
	installLegacyRollupForTest(t, s, day)
	installLegacyRollupForTest(t, s, week)
	installLegacyRollupForTest(t, s, month)
	installLegacyRollupForTest(t, s, year)
	if err := s.DeleteObservationRecord(ctx, private); err != nil {
		t.Fatalf("input-only source erasure exceeded derivative transaction budget: %v", err)
	}
	for _, id := range []string{day.ID, week.ID, month.ID, year.ID} {
		r, found, err := s.GetChronicleRollup(ctx, id)
		if err != nil || !found || r.Text != "" || len(r.ChildIDs) != 0 || len(r.SourceIDs) != 0 || !r.Dirty {
			t.Fatalf("erased input retained public derivative data: %s %+v %v %v", id, r, found, err)
		}
	}
}

func TestRollupSourceRecursAcrossYearsAndErasesAfterReopen(t *testing.T) {
	ctx := context.Background()
	s, path, _ := rollupTestStore(t, 0xc7)
	start := time.Date(2018, 7, 1, 9, 0, 0, 0, time.UTC)
	source := rollupTestSource(t, s, "Recurring project evidence referenced throughout the saved history.", start)
	const count = 530
	var ids []string
	for n := 0; n < count; n++ {
		at := start.AddDate(0, 0, n)
		id := "day:" + at.Format("2006-01-02")
		r := rollupTestRecord(t, id, "- Reviewed recurring project evidence.", []string{"hour:" + at.Format("20060102T15Z")}, []string{source})
		if err := s.PutChronicleRollup(ctx, r); err != nil {
			t.Fatalf("publication%d stranded a recurring source: %v", n+1, err)
		}
		ids = append(ids, id)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = OpenDurable(path, durableTestKey(t, 0xc7), OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.DeleteObservationRecord(ctx, source); err != nil {
		t.Fatalf("historical erasure failed: %v", err)
	}
	for _, id := range ids {
		r, found, err := s.GetChronicleRollup(ctx, id)
		if err != nil || !found || r.Text != "" || len(r.SourceIDs) != 0 || !r.Dirty {
			t.Fatalf("historical source survived in%s: found%v row%+v err%v", id, found, r, err)
		}
	}
}
