package durable

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
)

type activityPendingFixture interface {
	StageActivitySegment(context.Context, activity.Segment) error
	PeekPendingActivity(context.Context) (activity.Segment, bool, error)
	DiscardPendingActivity(context.Context, string) error
}

func TestActivityPendingDeletionRejectsLateReplay(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe6), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	source, err := capture.SanitizeObservation("reader.app", "", "Private source whose pending interval must be erased.", 512)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(ctx, source, at); err != nil {
		t.Fatal(err)
	}
	segment := activity.Segment{ID: "erased-pending", WallStart: at, WallEnd: at.Add(time.Second), Duration: int64(time.Second), AppID: source.AppID(), SourceIDs: []string{source.ContentID()}}
	if err := s.StageActivitySegment(ctx, segment); err != nil {
		t.Fatal(err)
	}
	late, found, err := s.PeekPendingActivity(ctx)
	if err != nil || !found {
		t.Fatal("accepted work missing before deletion", err)
	}
	if err := s.DeleteObservationRecord(ctx, source.ContentID()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.PeekPendingActivity(ctx); err != nil || found {
		t.Fatalf("deleted source retained pending work: found=%v err=%v", found, err)
	}
	if _, found, err := s.readIndexValue(ctx, activityPendingNamespace, "segment:"+segment.ID, maxActivityRecordBytes+1024); err != nil || found {
		t.Fatalf("erasure retained encrypted pending source labels: found=%v err=%v", found, err)
	}
	for _, err := range []error{s.StageActivitySegment(ctx, late), s.PutActivitySegment(ctx, late)} {
		var rejected *storage.StoreError
		if !errors.As(err, &rejected) || rejected.Code != storage.Invalid {
			t.Fatalf("late pending interval recreated deleted source link: %v", err)
		}
	}
}

func TestActivityPendingAcceptsPublishedFirstPartBeforeItsSearchIndex(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xeb), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	header, parts, summary := makeStoredCapture(t, "A published document whose activity must precede search indexing.", at)
	stageStoredCapture(t, s, header, parts)
	receipt, err := s.PublishCaptureWithSourceReceipt(ctx, summary)
	if err != nil || !receipt.Committed || receipt.SourceID == "" {
		t.Fatal("publication failed", err)
	}
	segment := activity.Segment{ID: "unindexed-publication-activity", WallStart: at, WallEnd: at.Add(time.Second), Duration: int64(time.Second), AppID: header.AppID(), Domain: header.Domain(), SourceIDs: []string{receipt.SourceID}}
	if err := s.StageActivitySegment(ctx, segment); err != nil {
		t.Fatal("accepted publication could not stage activity before its search index", err)
	}
	if err := s.PutActivitySegment(ctx, segment); err != nil {
		t.Fatal("accepted publication could not commit activity before its search index", err)
	}
	drainCaptureIndex(t, s)
	if _, err := s.DeleteCapturesBefore(ctx, nil, nil); err != nil {
		t.Fatal("first-part membership receipt broke bounded indexing or erasure", err)
	}
	if rows, err := s.GetActivitySegments(ctx, []string{segment.ID}); err != nil || len(rows) != 0 {
		t.Fatalf("publication deletion retained activity: %+v %v", rows, err)
	}
}

func TestActivityPendingPublishedPartErasureBeforeIndexRejectsLateReplay(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xec), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	header, parts, summary := makeStoredCapture(t, "A published activity interval erased before optional indexing.", at)
	stageStoredCapture(t, s, header, parts)
	receipt, err := s.PublishCaptureWithSourceReceipt(ctx, summary)
	if err != nil {
		t.Fatal(err)
	}
	segment := activity.Segment{ID: "erased-before-index", WallStart: at, WallEnd: at.Add(time.Second), Duration: int64(time.Second), AppID: header.AppID(), Domain: header.Domain(), SourceIDs: []string{receipt.SourceID}}
	if err := s.StageActivitySegment(ctx, segment); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteCapturesBefore(ctx, nil, nil); err != nil {
		t.Fatal("pre-index membership receipt broke source erasure", err)
	}
	if _, found, err := s.PeekPendingActivity(ctx); err != nil || found {
		t.Fatalf("pre-index capture erasure retained pending activity: found=%v err=%v", found, err)
	}
	if err := s.PutActivitySegment(ctx, segment); !storeCode(err, storage.Invalid) {
		t.Fatalf("late replay restored an erased unindexed publication: %v", err)
	}
}

func TestActivityPendingSurvivesEncryptedReopenAndCommitsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xe4)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	journal, ok := any(s).(activityPendingFixture)
	if !ok {
		t.Fatal("encrypted pending activity journal is unavailable")
	}
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	segments := []activity.Segment{
		{ID: "pending-first", WallStart: at, WallEnd: at.Add(time.Second), Duration: int64(time.Second), AppID: "reader.app"},
		{ID: "pending-second", WallStart: at.Add(time.Second), WallEnd: at.Add(2 * time.Second), Duration: int64(time.Second), AppID: "reader.app"},
	}
	for _, segment := range segments {
		if err := journal.StageActivitySegment(ctx, segment); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("pending-first")) || bytes.Contains(raw, []byte("reader.app")) {
		t.Fatal("pending activity labels or identifiers were persisted in plaintext")
	}
	clear(raw)
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	journal = any(s).(activityPendingFixture)
	for _, want := range segments {
		got, found, err := journal.PeekPendingActivity(ctx)
		if err != nil || !found || got.ID != want.ID || got.Duration != want.Duration {
			t.Fatalf("pending replay lost accepted interval: %+v %v %v", got, found, err)
		}
		if err := s.PutActivitySegment(ctx, got); err != nil {
			t.Fatal(err)
		}
		// Final persistence must acknowledge in the same transaction; there is
		// deliberately no separate discard between this write and the next peek.
	}
	if _, found, err := journal.PeekPendingActivity(ctx); err != nil || found {
		t.Fatalf("committed activity remained replayable: found=%v err=%v", found, err)
	}
	stored, err := s.GetActivitySegments(ctx, []string{"pending-first", "pending-second"})
	if err != nil || len(stored) != 2 {
		t.Fatalf("durable intervals missing: %+v %v", stored, err)
	}
}
