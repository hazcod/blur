package durable

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/power"
)

func settleCalendarWork(t *testing.T, s *Store) {
	t.Helper()
	w := historicalRollupWorker(s)
	defer w.Close()
	for pass := 0; pass < 64; pass++ {
		if err := w.ProcessRollups(context.Background()); err != nil {
			t.Fatal(err)
		}
		status, err := s.ChronicleRollupWorkStatus(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status.Dirty == 0 && !status.BackfillPending {
			return
		}
	}
	t.Fatal("calendar work did not settle")
}

type calendarBudgetGenerator struct {
	reviewFoundationModel
	calls atomic.Int32
}

func (g *calendarBudgetGenerator) GenerateRollup(ctx context.Context, prompt chronicle.RollupPrompt, limit int) (string, error) {
	g.calls.Add(1)
	return g.reviewFoundationModel.GenerateRollup(ctx, prompt, limit)
}

func TestCalendarParentsProgressUnderUnrelatedDayChurnWithinSixCalls(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xb3)
	putHistoricalHour(t, s, time.Date(2018, 2, 6, 9, 0, 0, 0, time.UTC))
	settleCalendarWork(t, s)
	beforeMonth, _, _ := s.GetChronicleRollup(ctx, "month:2018-02")
	beforeYear, _, _ := s.GetChronicleRollup(ctx, "year:2018")
	hour, _, _ := s.GetChronicle(ctx, "hour:20180206T09Z")
	corrected := rollupTestSource(t, s, "Corrected source for the calendar release decision.", hour.Start.Add(time.Minute))
	hour.Text = "Corrected calendar release decision."
	hour.SourceIDs = []string{corrected}
	if err := s.PutChronicle(ctx, hour); err != nil {
		t.Fatal(err)
	}
	var hot []chronicle.Summary
	for n := 1; n <= 8; n++ {
		at := time.Date(2019, 6, n, 9, 0, 0, 0, time.UTC)
		putHistoricalHour(t, s, at)
		row, _, err := s.GetChronicle(ctx, "hour:"+at.Format("20060102T15Z"))
		if err != nil {
			t.Fatal(err)
		}
		hot = append(hot, row)
	}
	g := &calendarBudgetGenerator{}
	w := chronicle.NewWorker(s, reviewFoundationPolicy{}, g, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider, Now: func() time.Time { return time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC) }, Zone: func() string { return rollupTestZone }})
	defer w.Close()
	for pass := 0; pass < 12; pass++ {
		for n := range hot {
			hot[n].Text += " Changed."
			if err := s.PutChronicle(ctx, hot[n]); err != nil {
				t.Fatal(err)
			}
		}
		g.calls.Store(0)
		if err := w.ProcessRollups(ctx); err != nil {
			t.Fatal(err)
		}
		if got := g.calls.Load(); got > 6 {
			t.Fatalf("calendar levels exceeded the shared inference budget: %d", got)
		}
		month, _, _ := s.GetChronicleRollup(ctx, "month:2018-02")
		year, _, _ := s.GetChronicleRollup(ctx, "year:2018")
		if month.Text != "" && year.Text != "" && !month.Dirty && !year.Dirty && month.InputHash != beforeMonth.InputHash && year.InputHash != beforeYear.InputHash {
			return
		}
	}
	t.Fatal("continuous unrelated day changes starved an old month or year")
}

func TestCalendarWorkerBackfillsCompletedLegacyHistory(t *testing.T) {
	ctx := context.Background()
	s, path, _ := rollupTestStore(t, 0xb1)
	var sources []string
	for _, at := range []time.Time{
		time.Date(2024, 2, 29, 9, 0, 0, 0, time.UTC),
		time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC),
		time.Date(2024, 12, 31, 9, 0, 0, 0, time.UTC),
	} {
		sources = append(sources, putHistoricalHour(t, s, at))
	}
	// Reproduce an older schema-7 store with completed day/week discovery.
	// Remove only calendar placeholders from a current writer's fixture.
	if err := s.Update(ctx, func(tx *Tx) error {
		for _, id := range []string{"month:2024-02", "month:2024-03", "month:2024-12", "year:2024"} {
			if err := tx.deleteRollup(id); err != nil {
				return err
			}
		}
		epoch, err := s.rollupEpochReader(tx.writer)
		if err != nil {
			return err
		}
		return tx.PutIndexRecord(rollupWorkNS, "backfill", mustJSON(rollupWorkBackfill{Version: 1, Epoch: epoch, Zone: rollupTestZone, Phase: 2}))
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = OpenDurable(path, durableTestKey(t, 0xb1), OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetChronicleZone(func() string { return rollupTestZone })
	settleCalendarWork(t, s)
	for n, month := range []string{"02", "03", "12"} {
		row, found, err := s.GetChronicleRollup(ctx, "month:2024-"+month)
		wantChild := map[string]string{"02": "day:2024-02-29", "03": "day:2024-03-01", "12": "day:2024-12-31"}[month]
		if err != nil || !found || row.Dirty || row.Text == "" || row.Sampled || !slices.Equal(row.SourceIDs, []string{sources[n]}) || !slices.Equal(row.ChildIDs, []string{wantChild}) {
			t.Fatalf("historical calendar month stranded or misattributed: %+v found%v err%v", row, found, err)
		}
	}
	sort.Strings(sources)
	row, found, err := s.GetChronicleRollup(ctx, "year:2024")
	if err != nil || !found || row.Dirty || row.Text == "" || row.Sampled || !slices.Equal(row.SourceIDs, sources) || !slices.Equal(row.ChildIDs, []string{"month:2024-02", "month:2024-03", "month:2024-12"}) {
		t.Fatalf("calendar year did not use its months: %+v found%v err%v", row, found, err)
	}
	rows, next, err := s.ListChronicleRollups(ctx, "month", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), "", 2)
	if err != nil || len(rows) != 2 || next != "month:2024-03" {
		t.Fatalf("calendar pagination failed: %+v %q %v", rows, next, err)
	}
}

func TestCalendarAncestorsFenceChangedHoursAndEraseOriginals(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xb2)
	source := putHistoricalHour(t, s, time.Date(2018, 2, 6, 9, 0, 0, 0, time.UTC))
	kept := putHistoricalHour(t, s, time.Date(2018, 3, 1, 9, 0, 0, 0, time.UTC))
	settleCalendarWork(t, s)
	for _, id := range []string{"month:2018-02", "year:2018"} {
		prior, found, err := s.GetChronicleRollup(ctx, id)
		if err != nil || !found || prior.Text == "" {
			t.Fatalf("missing ancestor before mutation: %s %+v %v", id, prior, err)
		}
		hour, _, err := s.GetChronicle(ctx, "hour:20180206T09Z")
		if err != nil {
			t.Fatal(err)
		}
		hour.Text += " Corrected decision."
		if err := s.PutChronicle(ctx, hour); err != nil {
			t.Fatal(err)
		}
		prior.Text = "- Stale ancestor after changed hourly input."
		if err := s.PutChronicleRollup(ctx, prior); err != nil {
			t.Fatal(err)
		}
		row, _, err := s.GetChronicleRollup(ctx, id)
		if err != nil || !row.Dirty || row.Revision <= prior.Revision || row.Text == prior.Text {
			t.Fatalf("late calendar generation survived changed hour: %+v %v", row, err)
		}
	}
	if err := s.DeleteObservationRecord(ctx, source); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"day:2018-02-06", "week:2018-W06", "month:2018-02", "year:2018"} {
		row, found, err := s.GetChronicleRollup(ctx, id)
		if err != nil || !found || row.Text != "" || len(row.ChildIDs)+len(row.SourceIDs) != 0 || !row.Dirty {
			t.Fatalf("erased input survived in a public ancestor: %s %+v %v", id, row, err)
		}
	}
	settleCalendarWork(t, s)
	year, found, err := s.GetChronicleRollup(ctx, "year:2018")
	if err != nil || !found || year.Dirty || year.Text == "" || !slices.Equal(year.SourceIDs, []string{kept}) || !slices.Equal(year.ChildIDs, []string{"month:2018-03"}) || strings.Contains(year.Text, "Stale ancestor") {
		t.Fatalf("calendar regeneration restored obsolete evidence: %+v %v %v", year, found, err)
	}
}
