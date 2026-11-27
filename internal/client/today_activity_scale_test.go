package client

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/today"
)

type largeTodayActivityFixture struct{ rows []activity.Segment }

func (f *largeTodayActivityFixture) ListActivitySegmentsForDates(context.Context, []string, int) ([]activity.Segment, error) {
	return nil, ErrActivityLimit
}

func (f *largeTodayActivityFixture) ListActivitySegmentsForDate(_ context.Context, _ string, after string, limit int) ([]activity.Segment, string, error) {
	start := 0
	for start < len(f.rows) && f.rows[start].ID <= after {
		start++
	}
	end := min(start+limit, len(f.rows))
	rows := append([]activity.Segment(nil), f.rows[start:end]...)
	if end < len(f.rows) && len(rows) > 0 {
		return rows, rows[len(rows)-1].ID, nil
	}
	return rows, "", nil
}

type todaySnapshotFixture struct{ snapshot today.Snapshot }

func (w *todaySnapshotFixture) PutTodaySnapshot(_ context.Context, snapshot today.Snapshot) error {
	w.snapshot = snapshot.Clone()
	return nil
}

func TestTodayMaterializesActivityBeyondTheFormerSinglePageLimit(t *testing.T) {
	const total = MaxActivityReportSegments + 1
	date := "2026-11-20"
	start, _ := time.Parse(time.DateOnly, date)
	rows := make([]activity.Segment, total)
	for i := range rows {
		at := start.Add(time.Duration(i) * time.Second)
		rows[i] = activity.Segment{
			ID: fmt.Sprintf("segment-%05d", i), WallStart: at, WallEnd: at.Add(time.Second), Duration: int64(time.Second),
			AppID: "com.apple.terminal", SourceIDs: []string{fmt.Sprintf("%064x", i+1)},
		}
	}
	reader := &largeTodayActivityFixture{rows: rows}
	writer := &todaySnapshotFixture{}
	view, found, err := materializeTodayActivity(context.Background(), date, "UTC", today.Snapshot{}, false, reader, writer)
	if err != nil || !found || view.Date != date || writer.snapshot.GenerationID == "" {
		t.Fatalf("Today failed to compact all paged activity: found=%t view=%+v err=%v", found, view, err)
	}
}
