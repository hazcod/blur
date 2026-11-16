package durable

import (
	"context"
	"github.com/hazcod/blur/internal/chronicle"
	"strings"
	"testing"
	"time"
)

func TestCapturePartRecapsUseActualRevisitsAndEraseAllReferencedHours(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	at := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Hour).Add(time.Minute)
	text := strings.Repeat("Reviewed the Northstar compiler implementation.\n", 1500) + "Mira owns the rollback."
	var summaryID, sourceID string
	var hours []string
	for i := range 4 {
		when := at.Add(time.Duration(i) * 24 * time.Hour)
		h, parts, summary := makeStoredCapture(t, text, when)
		stageStoredCapture(t, s, h, parts)
		receipt, err := s.PublishCaptureWithSourceReceipt(ctx, summary)
		if err != nil {
			t.Fatal(err)
		}
		summaryID = summary.ContentID()
		if sourceID != "" && sourceID != receipt.SourceID {
			t.Fatal("duplicate identity changed")
		}
		sourceID = receipt.SourceID
		for range 32 {
			more, err := s.IndexPendingCaptures(ctx, 16)
			if err != nil {
				t.Fatal(err)
			}
			if !more {
				break
			}
		}
		start := when.Truncate(time.Hour)
		id := observationTimelineHourID(start)
		hours = append(hours, id)
		recap := chronicle.Summary{HourID: id, Start: start, End: start.Add(time.Hour), Text: "Reviewed Northstar compiler work.", SourceIDs: []string{sourceID}}
		if err = s.PutChronicle(ctx, recap); err != nil {
			t.Fatal("actual document revisit rejected from recap", i, err)
		}
	}
	for _, id := range hours {
		if _, found, err := s.GetChronicle(ctx, id); err != nil || !found {
			t.Fatal("accepted recap unreadable", id, found, err)
		}
	}
	if err := s.DeleteObservationRecord(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetCapture(ctx, summaryID); err != nil || found {
		t.Fatal("capture retained", found, err)
	}
	for _, id := range hours {
		if _, found, err := s.GetChronicle(ctx, id); err != nil || found {
			t.Fatal("part-derived historical recap survived", id, found, err)
		}
	}
}
