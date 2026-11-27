package durable

import (
	"context"
	"github.com/hazcod/blur/internal/chronicle"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestReviewHistoricalSixtyFourCapturePartsFitHourlyAdmission(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xe5)
	at := time.Date(2026, 11, 15, 9, 10, 0, 0, time.UTC)
	summary := publishIndexedFixture(t, s, strings.Repeat("Reviewed evidence.\n", 120000), at)
	drainCaptureIndex(t, s)
	publishIndexedFixture(t, s, strings.Repeat("Reviewed evidence.\n", 120000), at.Add(24*time.Hour))
	drainCaptureIndex(t, s)
	ids, _, err := s.ListCaptureSourceIDs(ctx, summary.ContentID(), -1, 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 64 {
		t.Fatalf("fixture needs64parts, got%d", len(ids))
	}
	sort.Strings(ids)
	start := at.Truncate(time.Hour)
	row := chronicle.Summary{HourID: "hour:" + start.Format("20060102T15Z"), Start: start, End: start.Add(time.Hour), Text: "Reviewed source evidence.", SourceIDs: ids}
	if err := s.PutChronicle(ctx, row); err != nil {
		t.Fatalf("valid64part hour exceeds metadata admission operation budget: %v", err)
	}
}
