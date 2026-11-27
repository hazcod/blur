package durable

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
)

func TestTimelineStorageMetadataIsIndependentOfReadScanBudget(t *testing.T) {
	meta := observationTimelineMeta{pages: 1025, count: 65537}
	got, err := decodeObservationTimelineMeta(encodeObservationTimelineMeta(meta))
	if err != nil || got != meta {
		t.Fatalf("read work budget became a storage ceiling: %+v %v", got, err)
	}
	hour := observationTimelineHourID(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	if gotHour, page, err := parseObservationTimelinePageID(observationTimelinePageID(hour, 1024)); err != nil || gotHour != hour || page != 1024 {
		t.Fatalf("valid continuation beyond old page ceiling rejected: %q %d %v", gotHour, page, err)
	}
}

// Exercise the rollover writer at its former boundary without building 65,536
// unrelated primary sources. Both writers access only the authenticated tail
// page plus metadata; the earlier pages are outside this local operation.
func TestTimelineWritersContinuePastFormerHourCapacity(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xea), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	at := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	hour := observationTimelineHourID(at)
	oldTail := make([]captureWindowCandidate, observationTimelinePageSize)
	for i := range oldTail {
		oldTail[i] = captureWindowCandidate{id: fmt.Sprintf("%064x", i+1), at: at.Add(time.Duration(i) * time.Second)}
	}
	for _, namespace := range []string{observationTimelineNS, captureTimelineNS} {
		t.Run(namespace, func(t *testing.T) {
			err := store.Update(ctx, func(tx *Tx) error {
				if err := tx.PutIndexRecord(namespace, hour, encodeObservationTimelineMeta(observationTimelineMeta{pages: 1024, count: 65536})); err != nil {
					return err
				}
				if err := tx.PutIndexRecord(namespace, observationTimelinePageID(hour, 1023), encodeObservationTimelinePage(oldTail)); err != nil {
					return err
				}
				source, err := capture.SanitizeObservation("terminal.app", "", "Agent tail beyond prior timeline write ceiling", 1024)
				if err != nil {
					return err
				}
				if namespace == observationTimelineNS {
					_, err = tx.appendObservationTimelineLocked(source, at.Add(2*time.Minute))
				} else {
					err = tx.indexCaptureTimelineLocked(source.ContentID(), at.Add(2*time.Minute))
				}
				return err
			})
			if err != nil {
				t.Fatal("accepted next timeline row was rejected at a read budget", err)
			}
			encoded, found, err := store.readIndexValue(ctx, namespace, hour, 9)
			if err != nil || !found {
				t.Fatal(err)
			}
			meta, err := decodeObservationTimelineMeta(encoded)
			if err != nil || meta.pages != 1025 || meta.count != 65537 {
				t.Fatal("rollover lost cumulative metadata", meta, err)
			}
			for _, tc := range []struct{ page, count int }{{1023, 64}, {1024, 1}} {
				encoded, found, err := store.readIndexValue(ctx, namespace, observationTimelinePageID(hour, tc.page), maxObservationTimelinePage)
				if err != nil || !found {
					t.Fatal(err)
				}
				rows, err := decodeObservationTimelinePage(encoded, hour)
				if err != nil || len(rows) != tc.count {
					t.Fatal("rollover lost old or new tail", tc.page, len(rows), err)
				}
			}
		})
	}
}
