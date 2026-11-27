package durable

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
)

func TestActivitySourceLinksPreserveEveryIntervalAndEraseBeyondLegacyPage(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe5), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	source, err := capture.SanitizeObservation("reader.app", "", "Source revisited across many separate activity intervals.", 512)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(ctx, source, at); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 130; i++ {
		start := at.Add(time.Duration(2*i) * time.Second)
		id := fmt.Sprintf("many-intervals-%03d", i)
		segment := activity.Segment{ID: id, WallStart: start, WallEnd: start.Add(time.Second), Duration: int64(time.Second), AppID: "reader.app", SourceIDs: []string{source.ContentID()}}
		if err := s.PutActivitySegment(ctx, segment); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for first := 0; first < len(ids); first += 32 {
		stored, err := s.GetActivitySegments(ctx, ids[first:min(first+32, len(ids))])
		if err != nil || len(stored) != min(32, len(ids)-first) {
			t.Fatal("accepted activity interval missing", err)
		}
		for _, segment := range stored {
			if len(segment.SourceIDs) != 1 || segment.SourceIDs[0] != source.ContentID() {
				t.Fatalf("accepted source link was stripped: interval=%s", segment.ID)
			}
		}
	}
	if err := s.DeleteObservationRecord(ctx, source.ContentID()); err != nil {
		t.Fatal("unbounded source references did not drain in bounded erasure steps", err)
	}
	for first := 0; first < len(ids); first += 32 {
		stored, err := s.GetActivitySegments(ctx, ids[first:min(first+32, len(ids))])
		if err != nil || len(stored) != 0 {
			t.Fatal("source deletion retained later activity references", err)
		}
	}
}

func TestActivitySourceLinksReadLegacyArrayAndAppendWithoutLosingItsReferences(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xe9)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	source, err := capture.SanitizeObservation("reader.app", "", "Source whose historical activity reverse index uses the legacy array.", 512)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(ctx, source, at); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"legacy-a", "legacy-b"} {
		start := at.Add(time.Duration(i*2) * time.Second)
		if err := s.PutActivitySegment(ctx, activity.Segment{ID: id, WallStart: start, WallEnd: start.Add(time.Second), Duration: int64(time.Second), AppID: "reader.app", SourceIDs: []string{source.ContentID()}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		for _, id := range []string{"legacy-a", "legacy-b"} {
			if err := tx.deleteKnowledgeRecord(activitySourceNodesNamespace, activitySourceNodeID(source.ContentID(), id)); err != nil {
				return err
			}
		}
		return tx.PutIndexRecord(activitySourceNamespace, source.ContentID(), mustJSON([]string{"legacy-a", "legacy-b"}))
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	start := at.Add(10 * time.Second)
	if err := s.PutActivitySegment(ctx, activity.Segment{ID: "new-reference", WallStart: start, WallEnd: start.Add(time.Second), Duration: int64(time.Second), AppID: "reader.app", SourceIDs: []string{source.ContentID()}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObservationRecord(ctx, source.ContentID()); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.GetActivitySegments(ctx, []string{"legacy-a", "legacy-b", "new-reference"}); err != nil || len(rows) != 0 {
		t.Fatalf("mixed legacy/new references survived source erasure: %+v %v", rows, err)
	}
}
