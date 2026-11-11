package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/storage"
)

func TestActivitySegmentsAreEncryptedImmutableAndReopenable(t *testing.T) {
	ctx := context.Background()
	path := durableTestPath(t)
	key := durableTestKey(t, 0x62)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	segment := activity.Segment{ID: "segment-01", WallStart: start, WallEnd: start.Add(time.Minute), Duration: int64(time.Minute), AppID: "slack", Domain: "work.example", Project: "project-alpha", RuleID: "rule-01", RuleVersion: 2, SourceIDs: []string{"source-01"}}
	if err := store.PutActivitySegment(ctx, segment); err != nil {
		t.Fatal(err)
	}
	if err := store.PutActivitySegment(ctx, segment); !storeCode(err, storage.Duplicate) {
		t.Fatalf("duplicate segment error=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	physical, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{[]byte(segment.ID), []byte(segment.AppID), []byte(segment.Domain), []byte(segment.Project), []byte(segment.SourceIDs[0])} {
		if bytes.Contains(physical, secret) {
			t.Fatalf("plaintext activity metadata persisted: %q", secret)
		}
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.GetActivitySegments(ctx, []string{"absent", segment.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != segment.ID || got[0].Project != segment.Project || got[0].SourceIDs[0] != segment.SourceIDs[0] {
		t.Fatalf("reopened activity segment=%#v", got)
	}
	if _, err := store.GetActivitySegments(ctx, make([]string, 65)); !storeCode(err, storage.Invalid) {
		t.Fatalf("unbounded id list error=%v", err)
	}
}

func TestListActivitySegmentsForDateUsesStableBoundedPages(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x75), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	for i := range 65 {
		id := fmt.Sprintf("seg-%03d", i)
		segment := activity.Segment{ID: id, WallStart: start.Add(time.Duration(i) * time.Minute), WallEnd: start.Add(time.Duration(i+1) * time.Minute), Duration: int64(time.Minute), AppID: "editor", SourceIDs: []string{fmt.Sprintf("%064x", i+1)}}
		if err := store.PutActivitySegment(ctx, segment); err != nil {
			t.Fatal(err)
		}
	}
	first, next, err := store.ListActivitySegmentsForDate(ctx, "2026-11-05", "", 2)
	if err != nil || len(first) != 2 || first[0].ID != "seg-000" || first[1].ID != "seg-001" || next != "seg-001" {
		t.Fatalf("first date page=(%#v,%q,%v)", first, next, err)
	}
	second, next, err := store.ListActivitySegmentsForDate(ctx, "2026-11-05", "seg-062", 2)
	if err != nil || len(second) != 2 || second[0].ID != "seg-063" || second[1].ID != "seg-064" || next != "" {
		t.Fatalf("second date page=(%#v,%q,%v)", second, next, err)
	}
}

func TestListActivitySegmentsForDatesUsesOneBoundedRangeRead(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x76), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := range 65 {
		start := time.Date(2026, 11, 4+i/64, 23, 0, 0, 0, time.UTC).Add(time.Duration(i%64) * time.Minute)
		segment := activity.Segment{ID: fmt.Sprintf("range-%03d", i), WallStart: start, WallEnd: start.Add(time.Minute), Duration: int64(time.Minute), AppID: "editor"}
		if err := store.PutActivitySegment(ctx, segment); err != nil {
			t.Fatal(err)
		}
	}
	segments, err := store.ListActivitySegmentsForDates(ctx, []string{"2026-11-04", "2026-11-05"}, 128)
	if err != nil || len(segments) != 65 {
		t.Fatalf("range read returned %d segments, err=%v", len(segments), err)
	}
	if _, err := store.ListActivitySegmentsForDates(ctx, []string{"2026-11-05", "2026-11-04"}, 128); !storeCode(err, storage.Invalid) {
		t.Fatalf("unsorted UTC date partitions error=%v", err)
	}
}

func TestDeleteObservationRecordRemovesLinkedActivitySegments(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x73), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := repeatHex("a")
	start := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	segment := activity.Segment{ID: "activity-linked", WallStart: start, WallEnd: start.Add(time.Minute), Duration: int64(time.Minute), AppID: "editor", SourceIDs: []string{source}}
	if err := store.PutActivitySegment(ctx, segment); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		segmentStart := segment.WallEnd.Add(time.Duration(i) * time.Minute)
		repeated := activity.Segment{ID: fmt.Sprintf("activity-linked-repeat-%03d", i), WallStart: segmentStart, WallEnd: segmentStart.Add(time.Minute), Duration: int64(time.Minute), AppID: "editor", SourceIDs: []string{source}}
		if err := store.PutActivitySegment(ctx, repeated); err != nil {
			t.Fatalf("repeated activity segment %d: %v", i, err)
		}
	}
	coalesced, _, err := store.ListActivitySegmentsForDate(ctx, "2026-11-05", "", 64)
	if err != nil || len(coalesced) != 1 || coalesced[0].ID != segment.ID || coalesced[0].Duration != int64(101*time.Minute) {
		t.Fatalf("contiguous repeated-source activity was not coalesced: %#v err=%v", coalesced, err)
	}
	keptSource := repeatHex("d")
	kept := activity.Segment{ID: "activity-kept", WallStart: start.Add(time.Minute), WallEnd: start.Add(2 * time.Minute), Duration: int64(time.Minute), AppID: "editor", SourceIDs: []string{keptSource}}
	if err := store.PutActivitySegment(ctx, kept); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteObservationRecord(ctx, source); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetActivitySegments(ctx, []string{segment.ID})
	if err != nil || len(got) != 0 {
		t.Fatalf("activity segments after source deletion = %#v, %v; want deleted", got, err)
	}
	listed, cursor, err := store.ListActivitySegmentsForDate(ctx, "2026-11-05", "", 64)
	if err != nil || len(listed) != 1 || listed[0].ID != kept.ID || cursor != "" {
		t.Fatalf("activity date index after source deletion = (%#v,%q,%v), want unrelated segment", listed, cursor, err)
	}
	if err := store.DeleteObservationRecord(ctx, keptSource); err != nil {
		t.Fatal(err)
	}
	listed, cursor, err = store.ListActivitySegmentsForDate(ctx, "2026-11-05", "", 64)
	if err != nil || len(listed) != 0 || cursor != "" {
		t.Fatalf("empty activity date index = (%#v,%q,%v)", listed, cursor, err)
	}
	_, manifest, err := store.readOpenMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.verifyManifest(ctx, manifest); err != nil {
		t.Fatalf("manifest after activity cascade: %v", err)
	}
}

func TestDeleteObservationRecordFailsClosedWithoutProjectionIndexMarker(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x74), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	observation := schemaMigrationFixtureObservation(t)
	if err := store.PutObservation(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, func(tx *Tx) error {
		if err := tx.PutIndexRecord("storage-meta", "projection-index-v1", []byte{1}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, func(tx *Tx) error { return tx.deleteKnowledgeRecord("storage-meta", "projection-index-v1") }); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteObservationRecord(ctx, observation.ContentID()); !errors.Is(err, ErrProjectionIndexUnavailable) {
		t.Fatalf("delete without projection index marker error=%v, want ErrProjectionIndexUnavailable", err)
	}
	if got, found, err := store.GetObservation(ctx, observation.ContentID()); err != nil || !found || got.ContentID() != observation.ContentID() {
		t.Fatalf("source after refused deletion found=%t err=%v", found, err)
	}
}

func storeCode(err error, code storage.ErrorCode) bool {
	var e *storage.StoreError
	return errors.As(err, &e) && e.Code == code
}

// A source cited by more than maxSegmentsPerSource non-adjacent segments must
// not make writes fail: the extra segments persist without the source link.
func TestActivitySourceSegmentCapPersistsExtraSegmentsWithoutSourceLink(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x63), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	const total = maxSegmentsPerSource + 6
	for i := 0; i < total; i++ {
		// Gaps prevent coalescing, as when the user alternates between contexts.
		at := start.Add(time.Duration(2*i) * time.Minute)
		if err := store.PutActivitySegment(ctx, activity.Segment{ID: fmt.Sprintf("segment-%03d", i), WallStart: at, WallEnd: at.Add(time.Minute), Duration: int64(time.Minute), AppID: "slack", SourceIDs: []string{"source-01"}}); err != nil {
			t.Fatalf("segment %d: %v", i, err)
		}
	}
	var stored []activity.Segment
	cursor := ""
	for {
		page, next, err := store.ListActivitySegmentsForDate(ctx, "2026-11-05", cursor, 32)
		if err != nil {
			t.Fatal(err)
		}
		stored = append(stored, page...)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(stored) != total {
		t.Fatalf("stored %d segments, want %d", len(stored), total)
	}
	linked := 0
	for _, segment := range stored {
		if len(segment.SourceIDs) > 0 {
			linked++
		}
	}
	if linked != maxSegmentsPerSource {
		t.Fatalf("linked segments = %d, want %d", linked, maxSegmentsPerSource)
	}
	for _, segment := range stored {
		if segment.ID == fmt.Sprintf("segment-%03d", maxSegmentsPerSource) && len(segment.SourceIDs) != 0 {
			t.Fatalf("segment past the cap kept source link %v", segment.SourceIDs)
		}
	}
	var ids []string
	if err := store.Update(ctx, func(tx *Tx) error {
		encoded, found, err := tx.readIndexValue(activitySourceNamespace, "source-01", 8192)
		if err != nil || !found {
			return fmt.Errorf("source index found=%v err=%v", found, err)
		}
		return json.Unmarshal(encoded, &ids)
	}); err != nil {
		t.Fatal(err)
	}
	if len(ids) != maxSegmentsPerSource {
		t.Fatalf("source index has %d entries, want %d", len(ids), maxSegmentsPerSource)
	}
}

func TestEngineAlternatingStaticContentsNeverFailsActivityWrites(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x64), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := activity.New(activity.Config{MaxGap: 10 * time.Minute, RingSize: 16})
	wall := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	var mono int64
	step := func(event activity.Event) {
		t.Helper()
		segments, err := engine.Apply(event)
		if err != nil {
			t.Fatal(err)
		}
		for _, segment := range segments {
			if err := store.PutActivitySegment(ctx, segment); err != nil {
				t.Fatalf("PutActivitySegment: %v", err)
			}
		}
	}
	for i := 0; i < 70; i++ {
		for _, ev := range []struct {
			kind activity.EventKind
			src  string
			app  string
		}{{activity.Seen, "source-a", "app.a"}, {activity.Seen, "source-b", "app.b"}, {activity.Idle, "", ""}} {
			step(activity.Event{Kind: ev.kind, Monotonic: mono, Wall: wall, SourceID: ev.src, AppID: ev.app})
			mono += int64(time.Minute)
			wall = wall.Add(time.Minute)
		}
	}
}
