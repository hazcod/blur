package client

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/preferences"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestNativeCaptureActivityJournalRechecksPolicyOnReplayAndAllowsCleanup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := durable.OpenDurable(filepath.Join(dir, "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	prefs, err := preferences.NewStore(filepath.Join(dir, "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := preferences.DefaultConfig()
	config.CaptureEnabled = true
	if err := prefs.Save(ctx, config); err != nil {
		t.Fatal(err)
	}
	sink := &nativeCaptureCommitSink{store: store, preferences: prefs}
	source, err := capture.SanitizeObservation("com.apple.Terminal", "", "Synthetic queued agent activity", 1024)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if err := sink.PutObservationAt(ctx, source, at); err != nil {
		t.Fatal(err)
	}
	segment := activity.Segment{ID: "native-pending", AppID: source.AppID(), WallStart: at, WallEnd: at.Add(time.Second), Duration: int64(time.Second), SourceIDs: []string{source.ContentID()}}
	if err := sink.StageActivitySegment(ctx, segment); err != nil {
		t.Fatal(err)
	}
	pending, found, err := sink.PeekPendingActivity(ctx)
	if err != nil || !found || pending.ID != segment.ID {
		t.Fatalf("native forwarding lost durable acceptance: %+v %v %v", pending, found, err)
	}
	config.CaptureEnabled = false
	if err := prefs.Save(ctx, config); err != nil {
		t.Fatal(err)
	}
	for _, write := range []func() error{
		func() error { return sink.PutActivitySegment(ctx, pending) },
		func() error { return sink.StageActivitySegment(ctx, segment) },
	} {
		var rejected *storage.StoreError
		if err := write(); !errors.As(err, &rejected) || rejected.Code != storage.Invalid {
			t.Fatalf("changed policy did not permanently reject pending activity: %v", err)
		}
	}
	if err := sink.DiscardPendingActivity(ctx, pending.ID); err != nil {
		t.Fatal("pause blocked private pending cleanup", err)
	}
	if _, found, err := sink.PeekPendingActivity(ctx); err != nil || found {
		t.Fatalf("rejected work remained replayable: %v %v", found, err)
	}
	stored, err := store.GetActivitySegments(ctx, []string{segment.ID})
	if err != nil || len(stored) != 0 {
		t.Fatal("rejected pending work reached public activity", err)
	}
}

func TestNativeCaptureActivityJournalFallbackAcknowledgesOnlyActualWrite(t *testing.T) {
	ctx := context.Background()
	prefs, err := preferences.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := preferences.DefaultConfig()
	config.CaptureEnabled = true
	if err := prefs.Save(ctx, config); err != nil {
		t.Fatal(err)
	}
	spy := &nativePolicyStore{}
	sink := &nativeCaptureCommitSink{store: spy, preferences: prefs}
	if err := sink.StageActivitySegment(ctx, activity.Segment{AppID: "com.apple.Terminal"}); err != nil || spy.segments != 1 {
		t.Fatal("fallback acknowledged without actual sink write", err)
	}
	if _, found, err := sink.PeekPendingActivity(ctx); err != nil || found || spy.segments != 1 {
		t.Fatal("fallback created duplicate replay work", found, err)
	}
}
