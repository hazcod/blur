package ingest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

type eventSource struct {
	probe  platform.CaptureProbe
	events chan platform.CaptureEvent
}

type failFirstActivitySink struct {
	ActivitySink
	calls atomic.Int32
}

func TestSafeActivityWriteResultClassifiesDurableFailuresWithoutDetails(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{err: durable.ErrCorrupt, want: "storage_corrupt"},
		{err: durable.ErrTransaction, want: "storage_transaction"},
	} {
		if got := safeActivityWriteResult(tc.err); got != tc.want {
			t.Fatalf("safeActivityWriteResult(%T)=%q, want %q", tc.err, got, tc.want)
		}
	}
}

func (s *failFirstActivitySink) PutActivitySegment(ctx context.Context, segment activity.Segment) error {
	if s.calls.Add(1) == 1 {
		return errors.New("temporary activity storage failure")
	}
	return s.ActivitySink.PutActivitySegment(ctx, segment)
}

func (*eventSource) Capability(context.Context) (platform.CapabilityReport, error) {
	return platform.CapabilityReport{ID: "capture", State: platform.Available, Reason: "ready", Evidence: "test"}, nil
}
func (s *eventSource) Probe(context.Context) (platform.CaptureProbe, error) { return s.probe, nil }
func (s *eventSource) Next(ctx context.Context) (platform.CaptureEvent, error) {
	select {
	case event := <-s.events:
		return event, nil
	case <-ctx.Done():
		return platform.CaptureEvent{}, ctx.Err()
	}
}

func TestRunPersistsOnlySanitizedCurrentObservationsAndCoalescesDuplicates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &eventSource{
		probe:  platform.CaptureProbe{TargetID: "opaque", AppID: "org.example.editor"},
		events: make(chan platform.CaptureEvent, 1),
	}
	store := openTestStore(t)
	sink := &countingSink{store: store}
	observation, err := capture.SanitizeObservation(source.probe.AppID, "", "Design notes. password: verysecret", 256)
	if err != nil {
		t.Fatal(err)
	}
	calls := make(chan struct{}, 2)
	service, err := newService(source, captureFunc(func(context.Context) (capture.SanitizedObservation, error) {
		calls <- struct{}{}
		return observation, nil
	}), sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	select {
	case <-calls: // startup snapshot
	case <-time.After(time.Second):
		t.Fatal("startup capture did not run")
	}
	source.events <- platform.CaptureEvent{Kind: "focused_context_changed"}
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("focus change did not trigger capture")
	}
	waitFor(t, func() bool { return service.Stats().Duplicates == 1 })
	stored, found, err := store.GetObservation(context.Background(), observation.ContentID())
	if err != nil || !found || strings.Contains(stored.Text(), "verysecret") {
		t.Fatalf("durable output is not sanitized: found=%t text=%q err=%v", found, stored.Text(), err)
	}
	if service.Stats().Persisted != 1 {
		t.Fatalf("persisted=%d, want one deduplicated source", service.Stats().Persisted)
	}
	if got := sink.writes.Load(); got != 1 {
		t.Fatalf("identical focus frame caused %d durable writes, want one", got)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want cancellation", err)
	}
}

func TestCaptureChangeNotifiesOnlyAfterNewObservationIsPersisted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &eventSource{probe: platform.CaptureProbe{TargetID: "opaque", AppID: "org.example.editor"}, events: make(chan platform.CaptureEvent, 1)}
	store := openTestStore(t)
	observation, err := capture.SanitizeObservation(source.probe.AppID, "", "new note", 256)
	if err != nil {
		t.Fatal(err)
	}
	calls := make(chan struct{}, 2)
	changes := make(chan struct{}, 1)
	service, err := newServiceWithActivityNotify(source, captureFunc(func(context.Context) (capture.SanitizedObservation, error) {
		calls <- struct{}{}
		return observation, nil
	}), store, activity.New(activity.Config{MaxGap: time.Hour, RingSize: 16}), store, time.Now().Add(-time.Second), nil, func() {
		select {
		case changes <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("startup capture did not run")
	}
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("persisted observation did not notify")
	}
	source.events <- platform.CaptureEvent{Kind: "focused_context_changed"}
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("focus capture did not run")
	}
	waitFor(t, func() bool { return service.Stats().Duplicates == 1 })
	select {
	case <-changes:
		t.Fatal("duplicate observation emitted a change notification")
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestCaptureNotificationReceivesOnlyPersistedSanitizedObservationAndTimestamp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &eventSource{probe: platform.CaptureProbe{TargetID: "opaque", AppID: "org.example.editor"}, events: make(chan platform.CaptureEvent, 1)}
	store := openTestStore(t)
	observation, err := capture.SanitizeObservation(source.probe.AppID, "", "reviewed notes", 256)
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan capture.SanitizedObservation, 1)
	times := make(chan time.Time, 1)
	service, err := newServiceWithActivity(source, captureFunc(func(context.Context) (capture.SanitizedObservation, error) { return observation, nil }), store, activity.New(activity.Config{}), store, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	service.onObservation = func(o capture.SanitizedObservation, at time.Time) { events <- o; times <- at }
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	select {
	case got := <-events:
		if got.ContentID() != observation.ContentID() || got.Text() != observation.Text() {
			t.Fatal("notification did not match sanitized persisted source")
		}
	case <-time.After(time.Second):
		t.Fatal("missing post-persistence notification")
	}
	select {
	case at := <-times:
		if at.IsZero() || at.Location() != time.UTC {
			t.Fatalf("timestamp=%v", at)
		}
		page, err := store.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: 1, MaxBytes: 1024})
		if err != nil || len(page.CapturedAt) != 1 || !page.CapturedAt[0].Equal(at) {
			t.Fatalf("persisted timestamp mismatch: %v page=%+v err=%v", at, page, err)
		}
	case <-time.After(time.Second):
		t.Fatal("missing capture timestamp")
	}
	cancel()
	<-done
}

func TestNewRejectsUnencryptedSink(t *testing.T) {
	source := &eventSource{events: make(chan platform.CaptureEvent)}
	_, err := newService(source, captureFunc(func(context.Context) (capture.SanitizedObservation, error) {
		return capture.SanitizedObservation{}, nil
	}), unencryptedSink{}, nil)
	if !errors.Is(err, ErrUnencryptedSink) {
		t.Fatalf("New() error = %v, want ErrUnencryptedSink", err)
	}
}

func TestCaptureCancellationWinsBeforeDurableWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &eventSource{events: make(chan platform.CaptureEvent)}
	sink := &recordingSink{}
	observation, err := capture.SanitizeObservation("org.example.editor", "", "cancellation boundary", 256)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newService(source, captureFunc(func(context.Context) (capture.SanitizedObservation, error) {
		cancel()
		return observation, nil
	}), sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want cancellation", err)
	}
	if sink.writes != 0 {
		t.Fatalf("sink received %d write(s) after cancellation", sink.writes)
	}
}

func TestNewRejectsPipelineWithoutCaptureSource(t *testing.T) {
	_, err := New(capture.Pipeline{}, unencryptedSink{}, nil)
	if !errors.Is(err, ErrInvalidService) {
		t.Fatalf("New() error = %v, want ErrInvalidService", err)
	}
}

func TestRunPersistsSourceLinkedActivitySegmentsOnFocusEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &eventSource{probe: platform.CaptureProbe{TargetID: "opaque", AppID: "org.example.editor"}, events: make(chan platform.CaptureEvent, 1)}
	store := openTestStore(t)
	observation, err := capture.SanitizeObservation(source.probe.AppID, "", "project context", 256)
	if err != nil {
		t.Fatal(err)
	}
	engine := activity.New(activity.Config{MaxGap: time.Hour, RingSize: 16})
	calls := make(chan struct{}, 2)
	service, err := newServiceWithActivity(source, captureFunc(func(context.Context) (capture.SanitizedObservation, error) {
		calls <- struct{}{}
		return observation, nil
	}), store, engine, store, time.Now().Add(-time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("initial capture missing")
	}
	time.Sleep(time.Millisecond)
	source.events <- platform.CaptureEvent{Kind: "focused_context_changed"}
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("focus capture missing")
	}
	waitFor(t, func() bool { return len(engine.Segments()) > 0 })
	segments := engine.Segments()
	if len(segments) != 1 || len(segments[0].SourceIDs) != 1 || segments[0].SourceIDs[0] != observation.ContentID() {
		t.Fatalf("activity segment missing sanitized source link: %#v", segments)
	}
	var stored []activity.Segment
	waitFor(t, func() bool {
		stored, err = store.GetActivitySegments(context.Background(), []string{segments[0].ID})
		return err == nil && len(stored) == 1
	})
	if err != nil || stored[0].SourceIDs[0] != observation.ContentID() {
		t.Fatalf("durable activity segment=%#v err=%v", stored, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error=%v", err)
	}
}

func TestActivityWriteFailureDoesNotStopCaptureAndIsRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &eventSource{probe: platform.CaptureProbe{TargetID: "opaque", AppID: "org.example.editor"}, events: make(chan platform.CaptureEvent, 2)}
	store := openTestStore(t)
	sink := &failFirstActivitySink{ActivitySink: store}
	observation, err := capture.SanitizeObservation(source.probe.AppID, "", "working context", 256)
	if err != nil {
		t.Fatal(err)
	}
	calls := make(chan struct{}, 4)
	service, err := newServiceWithActivity(source, captureFunc(func(context.Context) (capture.SanitizedObservation, error) {
		calls <- struct{}{}
		return observation, nil
	}), store, activity.New(activity.Config{MaxGap: time.Hour, RingSize: 16}), sink, time.Now().Add(-time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("initial capture missing")
	}
	time.Sleep(time.Millisecond)
	source.events <- platform.CaptureEvent{Kind: "focused_context_changed"}
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("capture did not continue after first activity event")
	}
	waitFor(t, func() bool { return sink.calls.Load() >= 1 })
	select {
	case err := <-done:
		t.Fatalf("activity storage failure stopped capture: %v", err)
	default:
	}
	source.events <- platform.CaptureEvent{Kind: "focused_context_changed"}
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("capture did not continue for retry event")
	}
	waitFor(t, func() bool { return len(service.activity.Segments()) >= 2 })
	segments := service.activity.Segments()
	var stored []activity.Segment
	waitFor(t, func() bool {
		var getErr error
		stored, _, getErr = store.ListActivitySegmentsForDate(context.Background(), segments[0].WallStart.UTC().Format("2006-01-02"), "", 64)
		return getErr == nil && sink.calls.Load() >= 3 && len(stored) == 1 && stored[0].Duration >= segments[0].Duration+segments[1].Duration
	})
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want cancellation", err)
	}
}

func TestRunClosesActivitySessionOnPauseWithoutCountingPausedTime(t *testing.T) {
	store := openTestStore(t)
	observation, err := capture.SanitizeObservation("org.example.editor", "", "context", 256)
	if err != nil {
		t.Fatal(err)
	}
	engine := activity.New(activity.Config{MaxGap: time.Hour, RingSize: 16})
	epoch := time.Now().Add(-time.Second)
	source := &eventSource{probe: platform.CaptureProbe{TargetID: "opaque", AppID: "org.example.editor"}, events: make(chan platform.CaptureEvent, 1)}
	runOnce := func() (context.CancelFunc, <-chan error, <-chan struct{}) {
		ctx, cancel := context.WithCancel(context.Background())
		calls := make(chan struct{}, 1)
		service, err := newServiceWithActivity(source, captureFunc(func(context.Context) (capture.SanitizedObservation, error) {
			calls <- struct{}{}
			return observation, nil
		}), store, engine, store, epoch, nil)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- service.Run(ctx) }()
		select {
		case <-calls:
		case <-time.After(time.Second):
			t.Fatal("initial capture missing")
		}
		return cancel, done, calls
	}
	firstCancel, firstDone, _ := runOnce()
	time.Sleep(5 * time.Millisecond)
	firstCancel()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first Run() error=%v", err)
	}
	segments := engine.Segments()
	if len(segments) != 1 {
		t.Fatalf("pause should persist final activity segment, got %#v", segments)
	}
	stored, err := store.GetActivitySegments(context.Background(), []string{segments[0].ID})
	if err != nil || len(stored) != 1 {
		t.Fatalf("final activity segment was not persisted: %#v, %v", stored, err)
	}
	time.Sleep(5 * time.Millisecond)
	secondCancel, secondDone, _ := runOnce()
	// runOnce's call notification occurs inside Capture, before the service has
	// returned from Capture and applied the Seen event. Allow that event to
	// complete so the second close produces a second active segment.
	time.Sleep(5 * time.Millisecond)
	secondCancel()
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("second Run() error=%v", err)
	}
	got := engine.Segments()
	if len(got) != 2 || got[1].WallStart.Sub(got[0].WallEnd) < 5*time.Millisecond {
		t.Fatalf("pause interval must be a gap between active sessions, got %#v", got)
	}
}

type captureFunc func(context.Context) (capture.SanitizedObservation, error)

func (f captureFunc) Capture(ctx context.Context) (capture.SanitizedObservation, error) {
	return f(ctx)
}

type unencryptedSink struct{}

func (unencryptedSink) EncryptedAtRest() bool { return false }
func (unencryptedSink) PutObservation(context.Context, capture.SanitizedObservation) error {
	return nil
}

type recordingSink struct{ writes int }

func (*recordingSink) EncryptedAtRest() bool { return true }
func (s *recordingSink) PutObservation(context.Context, capture.SanitizedObservation) error {
	s.writes++
	return nil
}

type countingSink struct {
	store  *durable.Store
	writes atomic.Uint64
}

func (s *countingSink) EncryptedAtRest() bool { return true }
func (s *countingSink) PutObservation(ctx context.Context, observation capture.SanitizedObservation) error {
	s.writes.Add(1)
	return s.store.PutObservation(ctx, observation)
}

func openTestStore(t *testing.T) *durable.Store {
	t.Helper()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func waitFor(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for capture service")
}

type scriptedActivitySink struct {
	errs    []error
	written []string
}

func (*scriptedActivitySink) EncryptedAtRest() bool { return true }
func (s *scriptedActivitySink) PutActivitySegment(_ context.Context, segment activity.Segment) error {
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		if err != nil {
			return err
		}
	}
	s.written = append(s.written, segment.ID)
	return nil
}

func pendingTestSegments(ids ...string) []activity.Segment {
	start := time.Unix(1700000000, 0).UTC()
	out := make([]activity.Segment, 0, len(ids))
	for _, id := range ids {
		out = append(out, activity.Segment{ID: id, WallStart: start, WallEnd: start.Add(time.Second), Duration: int64(time.Second), SourceIDs: []string{"src-" + id}})
	}
	return out
}

func TestFlushPendingActivityDropsPermanentlyRejectedSegmentWithoutBlockingQueue(t *testing.T) {
	for _, code := range []storage.ErrorCode{storage.Invalid, storage.Duplicate} {
		var logs strings.Builder
		sink := &scriptedActivitySink{errs: []error{&storage.StoreError{Code: code}}}
		service := &Service{activitySink: sink, logger: slog.New(slog.NewTextHandler(&logs, nil)), pendingActivity: pendingTestSegments("a", "b", "c")}
		if err := service.flushPendingActivity(context.Background()); err != nil {
			t.Fatalf("%s: flush error = %v, want nil", code, err)
		}
		if got := strings.Join(sink.written, ","); got != "b,c" {
			t.Fatalf("%s: written = %q, want b,c", code, got)
		}
		if len(service.pendingActivity) != 0 {
			t.Fatalf("%s: queue = %d, want empty", code, len(service.pendingActivity))
		}
		if !strings.Contains(logs.String(), "dropped=1") || strings.Contains(logs.String(), "src-a") {
			t.Fatalf("%s: unexpected log %q", code, logs.String())
		}
	}
}

func TestFlushPendingActivityKeepsRetryableFailuresQueued(t *testing.T) {
	sink := &scriptedActivitySink{errs: []error{errors.New("transient")}}
	service := &Service{activitySink: sink, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), pendingActivity: pendingTestSegments("a", "b")}
	if err := service.flushPendingActivity(context.Background()); err == nil {
		t.Fatal("flush error = nil, want retryable error")
	}
	if len(service.pendingActivity) != 2 || len(sink.written) != 0 {
		t.Fatalf("queue=%d written=%v, want queue retained", len(service.pendingActivity), sink.written)
	}
}
