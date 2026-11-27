package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestPendingActivityPreservesEveryAcceptedSegmentBeyondOldQueueLimit(t *testing.T) {
	sink := &scriptedActivitySink{}
	s := &Service{activitySink: sink, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	engine := activity.New(activity.Config{})
	start := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var ids []string
	for n := 0; n < 258; n++ {
		segments, err := engine.Apply(activity.Event{Kind: activity.Seen, Monotonic: int64(n) * int64(time.Second), Wall: start.Add(time.Duration(n) * time.Second), AppID: "reader.app", SourceID: fmt.Sprintf("%064x", n+1)})
		if err != nil {
			t.Fatal(err)
		}
		for _, segment := range segments {
			ids = append(ids, segment.ID)
		}
		s.enqueueActivity(segments)
	}
	for pass := 0; pass < 16 && len(s.pendingActivity) > 0; pass++ {
		if err := s.flushPendingActivity(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(ids) != 257 || len(sink.written) != len(ids) {
		t.Fatalf("generated %d segments; persisted %d", len(ids), len(sink.written))
	}
	for i, id := range ids {
		if sink.written[i] != id {
			t.Fatalf("accepted segment %d was dropped or reordered", i)
		}
	}
}

func TestRunDrainsDurableActivityBacklogWithoutNewCaptureEvents(t *testing.T) {
	store := openTestStore(t)
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 80; i++ {
		start := at.Add(time.Duration(i) * time.Second)
		if err := store.StageActivitySegment(context.Background(), activity.Segment{ID: fmt.Sprintf("offline-%03d", i), WallStart: start, WallEnd: start.Add(time.Second), Duration: int64(time.Second), AppID: "reader.app"}); err != nil {
			t.Fatal(err)
		}
	}
	observation, err := capture.SanitizeObservation("reader.app", "", "A single initial capture while accepted activity replays.", 512)
	if err != nil {
		t.Fatal(err)
	}
	var captures atomic.Int32
	s, err := newServiceWithActivity(&eventSource{}, captureFunc(func(context.Context) (capture.SanitizedObservation, error) {
		captures.Add(1)
		return observation, nil
	}), store, activity.New(activity.Config{}), store, time.Now().Add(-time.Hour), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	doneConsumed := false
	defer func() {
		cancel()
		if !doneConsumed {
			<-done
		}
	}()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		stored, err := store.GetActivitySegments(context.Background(), []string{"offline-079"})
		if err != nil {
			t.Fatal(err)
		}
		if len(stored) == 1 {
			break
		}
		select {
		case err := <-done:
			doneConsumed = true
			t.Fatalf("capture stopped during durable replay: %v", err)
		case <-deadline.C:
			pending, found, err := store.PeekPendingActivity(context.Background())
			t.Fatalf("event-only capture left durable activity replay stranded after %d captures: pending=%s found=%v err=%v", captures.Load(), pending.ID, found, err)
		case <-poll.C:
		}
	}
	if captures.Load() != 1 {
		t.Fatalf("replay caused %d captures, want one initial capture", captures.Load())
	}
}

func TestPendingActivityBacklogReplaysAfterRestartWithoutLosingDurationsOrLinks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	store, err := durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	source, err := capture.SanitizeObservation("reader.app", "", "A saved source with hundreds of accepted activity intervals.", 512)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, source, at); err != nil {
		t.Fatal(err)
	}
	s := &Service{sink: store, activitySink: unavailableActivitySink{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	engine := activity.New(activity.Config{})
	for n := 0; n < 258; n++ {
		segments, err := engine.Apply(activity.Event{Kind: activity.Seen, Monotonic: int64(n) * int64(time.Second), Wall: at.Add(time.Duration(n) * time.Second), AppID: source.AppID(), SourceID: source.ContentID()})
		if err != nil {
			t.Fatal(err)
		}
		s.enqueueActivity(segments)
		if err := s.flushPendingActivity(ctx); err != nil || len(s.pendingActivity) != 0 {
			t.Fatalf("durably accepted activity accumulated in RAM: pending=%d err=%v", len(s.pendingActivity), err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	s = &Service{sink: store, activitySink: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for pass := 0; pass < 16; pass++ {
		if err := s.flushPendingActivity(ctx); err != nil {
			t.Fatal(err)
		}
		if _, found, err := store.PeekPendingActivity(ctx); err != nil {
			t.Fatal(err)
		} else if !found {
			break
		}
	}
	if _, found, err := store.PeekPendingActivity(ctx); err != nil || found {
		t.Fatalf("bounded replay did not finish backlog: found=%v err=%v", found, err)
	}
	segments, next, err := store.ListActivitySegmentsForDate(ctx, "2026-11-20", "", 64)
	if err != nil || next != "" || len(segments) != 1 || segments[0].Duration != int64(257*time.Second) || len(segments[0].SourceIDs) != 1 || segments[0].SourceIDs[0] != source.ContentID() {
		t.Fatalf("restart lost or duplicated accepted activity: %+v next=%s err=%v", segments, next, err)
	}
}

type unavailableActivitySink struct{}

func (unavailableActivitySink) EncryptedAtRest() bool { return true }
func (unavailableActivitySink) PutActivitySegment(context.Context, activity.Segment) error {
	return errors.New("storage unavailable")
}

func TestActivityStorageFailureStopsAdvancingBeforeAnotherCapture(t *testing.T) {
	observation, err := capture.SanitizeObservation("reader.app", "", "Enough context to persist this foreground document.", 512)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s, err := newServiceWithActivity(&eventSource{}, captureFunc(func(context.Context) (capture.SanitizedObservation, error) {
		calls++
		return observation, nil
	}), &recordingSink{}, activity.New(activity.Config{}), unavailableActivitySink{}, time.Now().Add(-time.Hour), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.captureCurrent(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.captureCurrent(context.Background()); !errors.Is(err, ErrActivityFailed) {
		t.Fatalf("unaccepted activity was acknowledged: %v", err)
	}
	segments := len(s.activity.Segments())
	if err := s.captureCurrent(context.Background()); !errors.Is(err, ErrActivityFailed) {
		t.Fatalf("blocked activity resumed advancing: %v", err)
	}
	if calls != 2 || len(s.activity.Segments()) != segments || len(s.pendingActivity) != 1 || s.health.State != HealthStorageError {
		t.Fatalf("failure did not backpressure capture: captures=%d segments=%d pending=%d health=%s", calls, len(s.activity.Segments()), len(s.pendingActivity), s.health.State)
	}
}
