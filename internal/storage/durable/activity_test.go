package durable

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nielsgroen/blur/internal/activity"
	"github.com/nielsgroen/blur/internal/storage"
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

func storeCode(err error, code storage.ErrorCode) bool {
	var e *storage.StoreError
	return errors.As(err, &e) && e.Code == code
}
