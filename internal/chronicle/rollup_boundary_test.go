package chronicle

import (
	"context"
	"github.com/hazcod/blur/internal/power"
	"strings"
	"testing"
	"time"
)

func TestReviewDayRejectsHourStraddlingLocalMidnight(t *testing.T) {
	f := newRollupFixture(t, power.LocalProvider, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	day, err := NewRollupPlaceholder("day:2026-11-15", "Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 15, 18, 0, 0, 0, time.UTC)
	// This hourly recap can describe 00:15 the following local day.
	const wrong = "At 00:15 on November16 deployed the next-day release."
	hour := Summary{HourID: "hour:" + start.Format("20060102T15Z"), Start: start, End: start.Add(time.Hour), Text: wrong, SourceIDs: []string{rollupSourceID(1)}}
	if err := f.repo.PutChronicle(context.Background(), hour); err != nil {
		t.Fatal(err)
	}
	if _, err := f.worker.refreshDayRollup(context.Background(), f.repo, f.model, day, loc, "Asia/Kolkata"); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.model.prompts {
		if strings.Contains(p.User, wrong) {
			t.Fatalf("out-of-period hour admitted to day ending %s: %s", day.End, p.User)
		}
	}
}
