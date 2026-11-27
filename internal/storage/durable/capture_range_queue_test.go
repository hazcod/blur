package durable

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func TestCaptureRangeQueuePreservesSmallCoreBesideOversizeOriginalWord(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	input := strings.Repeat("p", capture.MaxObservationTextBytes+capture.MaxCapturePartTextBytes-1) + "Wᆨ́ᆨ "
	publishIndexedFixture(t, s, input, time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC))
	drainCaptureIndex(t, s)
	page, err := s.SearchExact(ctx, scopedQuery("ᆨ"))
	if err != nil || len(page.Hits) != 1 || page.IndexingPending {
		t.Fatal("small supported core inherited a huge sibling's read budget", page, err)
	}
	e, found, err := s.GetSourceEvidence(ctx, page.Hits[0].SourceID)
	if err != nil || !found {
		t.Fatal("small original core is not inspectable", found, err)
	}
	span, ok := e.Provenance()
	if !ok || span.Start <= capture.MaxObservationTextBytes || span.End > int64(len(input)) || e.Text() != input[span.Start:span.End] {
		t.Fatal("small core lost its independent original range", span)
	}
	if page, err := s.SearchExact(ctx, scopedQuery("wᆨ")); err != nil || len(page.Hits) != 0 {
		t.Fatal("hydration context invented a suffix-token posting", page, err)
	}
}

func TestCaptureRangeQueueSurvivesRestartResetAndPartialErasure(t *testing.T) {
	for _, action := range []string{"resume", "reset", "delete"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			path, key := durableTestPath(t), durableTestKey(t, 0x98)
			s, err := OpenDurable(path, key, CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			summary := publishIndexedFixture(t, s, strings.Repeat(" ", capture.MaxCapturePartTextBytes-7)+"prefixWᆨ́ᆨ finished calibration.", time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC))
			var publication string
			reached := false
			for round := 0; round < 100 && !reached; round++ {
				if _, err := s.IndexPendingCaptures(ctx, 1); err != nil {
					t.Fatal(err)
				}
				if err := s.view(ctx, func(reader bolt.Reader) error {
					r, _, _, err := s.captureEditionReader(reader, summary.ContentID())
					if err != nil {
						return err
					}
					publication = r.Publication
					reached = r.IndexRangeQueued > 0 && r.IndexRangeWork != nil && r.IndexRangeWork.Stage == 2
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if !reached {
				t.Fatal("fixture did not checkpoint an active and queued distinct projection")
			}
			queueKey, coreKey := captureRangeQueueKey(summary.ContentID(), publication), captureRangeCoreKey(summary.ContentID(), publication)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenDurable(path, key, OpenExisting)
			if err != nil {
				t.Fatal(err)
			}
			if action == "reset" {
				if err := s.captureUpdate(ctx, func(tx *Tx) error {
					r, _, _, err := s.captureEditionReader(tx.writer, summary.ContentID())
					if err != nil {
						return err
					}
					r.IndexFormat = 5
					if err := tx.queueCaptureIndexLocked(summary.ContentID(), &r, summary.PartCount()); err != nil {
						return err
					}
					return tx.capturePutJSONLocked("edition:"+summary.ContentID(), r, captureMetaBytes)
				}); err != nil {
					t.Fatal(err)
				}
			}
			if action == "delete" {
				if err := s.DeleteObservationRecord(ctx, summary.ContentID()); err != nil {
					t.Fatal(err)
				}
			} else {
				drainCaptureIndex(t, s)
				if page, err := s.SearchExact(ctx, scopedQuery("ᆨ")); err != nil || len(page.Hits) != 1 {
					t.Fatal("queued exact core did not survive continuation", page, err)
				}
			}
			for _, key := range []string{queueKey, coreKey} {
				if _, found, err := s.readIndexValue(ctx, captureNS, key, captureRangeQueueBytes); err != nil || found {
					t.Fatal("finished/reset/erased projection retained pending metadata", key, found, err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenDurable(path, key, OpenExisting)
			if err != nil {
				t.Fatal(err)
			}
			if action == "delete" {
				if _, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil || found {
					t.Fatal("partial queue deletion restored original", found, err)
				}
			}
		})
	}
}
