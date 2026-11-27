package durable

import (
	"context"
	"fmt"
	"github.com/hazcod/blur/internal/chronicle"
	"testing"
	"time"
)

func TestIndependentQueueDoesNotPublishIncompleteBackfillWeekAsComplete(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xe1)
	for day := 5; day <= 11; day++ {
		putHistoricalHour(t, s, time.Date(2018, 2, day, 9, 0, 0, 0, time.UTC))
	}
	if err := s.DeleteAllChronicleRollups(ctx); err != nil {
		t.Fatal(err)
	}
	w := historicalRollupWorker(s)
	defer w.Close()
	if err := w.ProcessRollups(ctx); err != nil {
		t.Fatal(err)
	}
	row, found, err := s.GetChronicleRollup(ctx, "week:2018-W06")
	if err != nil {
		t.Fatal(err)
	}
	if found && row.Text != "" && !row.Dirty && !row.Sampled && row.CoverageVersion == chronicle.RollupCoverageVersion && len(row.ChildIDs) < 7 {
		t.Fatalf("backfill published incomplete saved week as complete: children=%v sampled=%v", row.ChildIDs, row.Sampled)
	}
}
func TestIndependentQueueStatusReportsEpochInvalidatedBackfill(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xe2)
	putHistoricalHour(t, s, time.Date(2018, 2, 6, 9, 0, 0, 0, time.UTC))
	for n := 0; n < 16; n++ {
		status, err := s.PrepareChronicleRollupWork(ctx, rollupTestZone)
		if err != nil {
			t.Fatal(err)
		}
		if !status.BackfillPending {
			break
		}
	}
	if err := s.DeleteAllChronicleRollups(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := s.ChronicleRollupWorkStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.BackfillPending {
		t.Fatalf("backfill falsely complete after epoch invalidation with retained hourly input: %+v", status)
	}
}
func TestIndependentQueueFencesOldZonePublicationBeforeBackfillReachesRow(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xe3)
	zone := "UTC"
	s.SetChronicleZone(func() string { return zone })
	dates := []time.Time{time.Date(2018, 1, 1, 9, 0, 0, 0, time.UTC), time.Date(2018, 1, 10, 9, 0, 0, 0, time.UTC), time.Date(2018, 1, 20, 9, 0, 0, 0, time.UTC), time.Date(2018, 2, 1, 9, 0, 0, 0, time.UTC), time.Date(2018, 2, 10, 9, 0, 0, 0, time.UTC)}
	var lastSource string
	for _, at := range dates {
		lastSource = putHistoricalHour(t, s, at)
	}
	if _, err := s.PrepareChronicleRollupWork(ctx, zone); err != nil {
		t.Fatal(err)
	}
	old, found, err := s.GetChronicleRollup(ctx, "day:2018-02-10")
	if err != nil || !found {
		t.Fatal(err)
	}
	generated := rollupTestRecord(t, old.ID, "- Old-zone generated recap.", []string{"hour:20180210T09Z"}, []string{lastSource})
	generated.Zone = old.Zone
	generated.Start = old.Start
	generated.End = old.End
	generated.Revision = old.Revision
	generated.Epoch = old.Epoch
	zone = "Europe/Brussels"
	if _, err := s.PrepareChronicleRollupWork(ctx, zone); err != nil {
		t.Fatal(err)
	}
	if err := s.PutChronicleRollup(ctx, generated); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.GetChronicleRollup(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text == generated.Text && got.Zone == "UTC" {
		t.Fatalf("old-zone generation accepted after new-zone backfill began: %+v", got)
	}
}

func TestIndependentQueueWeeksMakeProgressUnderHotDayChurn(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xe4)
	at := time.Date(2018, 2, 6, 9, 0, 0, 0, time.UTC)
	source := putHistoricalHour(t, s, at)
	old, _, _ := s.GetChronicleRollup(ctx, "day:2018-02-06")
	day := rollupTestRecord(t, old.ID, "- Ready old-week child.", []string{"hour:20180206T09Z"}, []string{source})
	day.CoverageVersion = chronicle.RollupCoverageVersion
	day.Revision = old.Revision
	day.Epoch = old.Epoch
	if err := s.PutChronicleRollup(ctx, day); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkChronicleRollupDirty(ctx, "week:2018-W06", rollupTestZone); err != nil {
		t.Fatal(err)
	}
	type hotInput struct {
		at     time.Time
		source string
	}
	var hot []hotInput
	for n := 1; n <= 6; n++ {
		at := time.Date(2019, 6, n, 9, 0, 0, 0, time.UTC)
		hot = append(hot, hotInput{at, putHistoricalHour(t, s, at)})
	}
	w := historicalRollupWorker(s)
	defer w.Close()
	for pass := 0; pass < 3; pass++ {
		for _, in := range hot {
			row := chronicle.Summary{HourID: "hour:" + in.at.Format("20060102T15Z"), Start: in.at, End: in.at.Add(time.Hour), Text: fmt.Sprintf("Current hour correction pass %d.", pass), SourceIDs: []string{in.source}}
			if err := s.PutChronicle(ctx, row); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.ProcessRollups(ctx); err != nil {
			t.Fatal(err)
		}
	}
	row, found, err := s.GetChronicleRollup(ctx, "week:2018-W06")
	if err != nil {
		t.Fatal(err)
	}
	if !found || row.Text == "" {
		t.Fatalf("ready week starved behind six day generations on every pass: found=%v row=%+v", found, row)
	}
}
