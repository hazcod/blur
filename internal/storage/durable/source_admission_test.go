package durable

import (
	"context"
	"fmt"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestIndependentLargeOriginalRollupAdmissionBudget(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xfc)
	var sources, children []string
	for h := 9; h <= 10; h++ {
		at := time.Date(2026, 11, 15, h, 10, 0, 0, time.UTC)
		var batch []string
		for n := 0; n < 8; n++ {
			prefix := fmt.Sprintf("Unique large source %d %d ", h, n)
			text := prefix + strings.Repeat("a", capture.MaxObservationTextBytes-len(prefix))
			o, err := capture.SanitizeObservation("com.example.editor", "", text, capture.MaxObservationTextBytes)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.PutObservationAt(ctx, o, at); err != nil {
				t.Fatal(err)
			}
			batch = append(batch, o.ContentID())
			sources = append(sources, o.ContentID())
		}
		sort.Strings(batch)
		start := at.Truncate(time.Hour)
		row := chronicle.Summary{HourID: "hour:" + start.Format("20060102T15Z"), Start: start, End: start.Add(time.Hour), Text: "Reviewed large source material.", SourceIDs: batch}
		if err := s.PutChronicle(ctx, row); err != nil {
			t.Fatalf("admit eight-source hour: %v", err)
		}
		children = append(children, row.HourID)
	}
	sort.Strings(sources)
	prior, found, err := s.GetChronicleRollup(ctx, "day:2026-11-15")
	if err != nil || !found {
		t.Fatal(err)
	}
	day := rollupTestRecord(t, prior.ID, "- Reviewed large source material.", children, sources)
	day.Revision = prior.Revision
	day.Epoch = prior.Epoch
	if err := s.PutChronicleRollup(ctx, day); err != nil {
		t.Fatalf("valid %d-source rollup exceeds body-read byte budget despite new64source cap: %v", len(sources), err)
	}
}
