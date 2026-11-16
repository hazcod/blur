// Package ingest connects passive platform events to sanitized encrypted
// observations. It never handles raw accessibility frames or invokes AI.
package ingest

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/storage"
)

var (
	ErrInvalidService  = errors.New("ingest: invalid service")
	ErrUnencryptedSink = errors.New("ingest: encrypted observation sink required")
	ErrAlreadyRun      = errors.New("ingest: service already run")
	ErrStoreWrite      = errors.New("ingest: encrypted observation write failed")
	ErrCaptureFailed   = errors.New("ingest: capture operation failed")
	ErrActivityFailed  = errors.New("ingest: activity accounting failed")
)

// Capturer exposes only the sanitized result of platform capture. Raw frames
// and untrusted text remain private to package capture.
type Capturer interface {
	Capture(context.Context) (capture.SanitizedObservation, error)
}

type Stats struct{ Persisted, Duplicates, Skipped, DroppedShort uint64 }

const maxPendingActivitySegments = 256

type ObservationSink interface {
	EncryptedAtRest() bool
	// PutObservation must honor ctx and abort an uncommitted write when canceled.
	PutObservation(context.Context, capture.SanitizedObservation) error
}

type ActivitySink interface {
	EncryptedAtRest() bool
	PutActivitySegment(context.Context, activity.Segment) error
}

// Service captures the currently open context at startup and after passive OS
// focus events. The durable store's atomic duplicate rejection is the final
// deduplication authority across restarts.
type Service struct {
	source          platform.CaptureSource
	capture         Capturer
	document        capture.DocumentJob
	onDocument      func(capture.CaptureSummary, bool)
	sink            ObservationSink
	logger          *slog.Logger
	activity        *activity.Engine
	activitySink    ActivitySink
	activityEpoch   time.Time
	pendingActivity []activity.Segment
	onChange        func()
	onObservation   func(capture.SanitizedObservation, time.Time)
	onCaptureCycle  func()
	onHealth        func(Health)
	health          Health
	refreshInterval time.Duration
	started         atomic.Bool
	lastContentID   string // Run is the sole writer; this stores a hash, never captured text.
	persisted       atomic.Uint64
	duplicates      atomic.Uint64
	skipped         atomic.Uint64
	droppedShort    atomic.Uint64
}

// SetCaptureCycleNotify registers a non-blocking callback after each
// event-driven capture attempt, including excluded apps. Configure it before Run.
func (s *Service) SetCaptureCycleNotify(notify func()) {
	if s == nil || s.started.Load() {
		return
	}
	s.onCaptureCycle = notify
}

// SetPeriodicRefresh enables a bounded fallback for content changes that do
// not emit focus notifications. Configure before Run; zero keeps event-only
// behavior for adapters without an idle-state reader.
func (s *Service) SetPeriodicRefresh(interval time.Duration) {
	if s == nil || s.started.Load() || interval != 0 && (interval < time.Second || interval > time.Minute) {
		return
	}
	s.refreshInterval = interval
}

// New binds event delivery to the exact source used by the capture pipeline,
// preventing startup from accidentally observing a different app's events.
func New(pipeline capture.Pipeline, sink ObservationSink, logger *slog.Logger) (*Service, error) {
	return newService(pipeline.Source, pipeline, sink, logger)
}

// NewWithActivity enables event-driven time accounting over sanitized capture
// results. The caller owns engine and epoch so a user pause/restart does not
// reset its monotonic clock or deletion state.
func NewWithActivity(pipeline capture.Pipeline, sink ObservationSink, engine *activity.Engine, activitySink ActivitySink, epoch time.Time, logger *slog.Logger) (*Service, error) {
	return newServiceWithActivity(pipeline.Source, pipeline, sink, engine, activitySink, epoch, logger)
}

// NewWithActivityAndNotify is NewWithActivity with a callback after a new
// sanitized observation has been durably stored. Callbacks run on the capture
// goroutine and should be nonblocking.
func NewWithActivityAndNotify(pipeline capture.Pipeline, sink ObservationSink, engine *activity.Engine, activitySink ActivitySink, epoch time.Time, logger *slog.Logger, onChange func()) (*Service, error) {
	service, err := newServiceWithActivity(pipeline.Source, pipeline, sink, engine, activitySink, epoch, logger)
	if err != nil {
		return nil, err
	}
	service.onChange = onChange
	return service, nil
}

// NewWithActivityAndObservationNotify adds a bounded in-process notification
// after persistence. The callback must be nonblocking; it receives sanitized
// evidence and the exact stored timestamp, never raw accessibility data.
func NewWithActivityAndObservationNotify(pipeline capture.Pipeline, sink ObservationSink, engine *activity.Engine, activitySink ActivitySink, epoch time.Time, logger *slog.Logger, onChange func(), onObservation func(capture.SanitizedObservation, time.Time)) (*Service, error) {
	service, err := NewWithActivityAndNotify(pipeline, sink, engine, activitySink, epoch, logger, onChange)
	if err != nil {
		return nil, err
	}
	service.onObservation = onObservation
	return service, nil
}

func newService(source platform.CaptureSource, capturer Capturer, sink ObservationSink, logger *slog.Logger) (*Service, error) {
	if source == nil || capturer == nil || sink == nil {
		return nil, ErrInvalidService
	}
	if !sink.EncryptedAtRest() {
		return nil, ErrUnencryptedSink
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{source: source, capture: capturer, sink: sink, logger: logger}, nil
}

func newServiceWithActivity(source platform.CaptureSource, capturer Capturer, sink ObservationSink, engine *activity.Engine, activitySink ActivitySink, epoch time.Time, logger *slog.Logger) (*Service, error) {
	return newServiceWithActivityNotify(source, capturer, sink, engine, activitySink, epoch, logger, nil)
}

func newServiceWithActivityNotify(source platform.CaptureSource, capturer Capturer, sink ObservationSink, engine *activity.Engine, activitySink ActivitySink, epoch time.Time, logger *slog.Logger, onChange func()) (*Service, error) {
	if engine == nil || activitySink == nil || epoch.IsZero() {
		return nil, ErrInvalidService
	}
	if !activitySink.EncryptedAtRest() {
		return nil, ErrUnencryptedSink
	}
	service, err := newService(source, capturer, sink, logger)
	if err != nil {
		return nil, err
	}
	service.activity, service.activitySink, service.activityEpoch = engine, activitySink, epoch
	service.onChange = onChange
	return service, nil
}

// Run is event-driven: one initial snapshot and then one snapshot per
// coalesced focus event. Errors from individual frames are counted and skipped;
// primary observation storage errors stop the service rather than silently
// losing evidence. Activity reporting is secondary and is retried separately.
func (s *Service) Run(ctx context.Context) (runErr error) {
	if s == nil || ctx == nil {
		return ErrInvalidService
	}
	if !s.started.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	defer s.closeDocument()
	if s.activity != nil {
		defer func() {
			if err := s.closeActivitySession(); err != nil {
				if runErr == nil {
					runErr = err
				} else {
					runErr = errors.Join(runErr, err)
				}
			}
		}()
	}
	if err := s.captureCurrent(ctx); err != nil {
		return err
	}
	for {
		waitCtx, cancelWait := ctx, func() {}
		interval := s.refreshInterval
		if s.document != nil {
			interval = 25 * time.Millisecond
		}
		if interval > 0 {
			waitCtx, cancelWait = context.WithTimeout(ctx, interval)
		}
		event, err := s.source.Next(waitCtx)
		refreshDue := errors.Is(waitCtx.Err(), context.DeadlineExceeded)
		cancelWait()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if refreshDue && errors.Is(err, context.DeadlineExceeded) {
				if err := s.captureCurrent(ctx); err != nil {
					return err
				}
				continue
			}
			s.logger.Warn("capture event source stopped", "operation", "capture", "result", "unavailable")
			return ErrCaptureFailed
		}
		switch event.Kind {
		case "frontmost_app_changed", "focused_context_changed":
			// A progressive read can span many rounds. Stop attributing its
			// acquisition time to the prior app. Ordinary bounded captures keep
			// their existing contiguous activity accounting.
			if _, streams := s.capture.(DocumentCapturer); streams {
				if _, stages := s.sink.(DocumentSink); stages {
					s.pauseActivity(ctx)
				}
			}
			s.closeDocument()
			if err := s.captureCurrent(ctx); err != nil {
				return err
			}
		default:
			s.skipped.Add(1)
		}
	}
}

func (s *Service) closeActivitySession() error {
	if s == nil || s.activity == nil || s.activitySink == nil {
		return nil
	}
	now := time.Now()
	segments, err := s.activity.Apply(activity.Event{Kind: activity.Idle, Monotonic: now.Sub(s.activityEpoch).Nanoseconds(), Wall: now})
	if err != nil {
		s.logger.Error("activity session close rejected", "operation", "activity", "result", "invalid")
		return ErrActivityFailed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.enqueueActivity(segments)
	if err := s.flushPendingActivity(ctx); err != nil {
		s.logger.Error("final encrypted activity write failed", "operation", "activity", "result", safeActivityWriteResult(err))
		return ErrActivityFailed
	}
	return nil
}

func (s *Service) enqueueActivity(segments []activity.Segment) {
	for _, segment := range segments {
		segment.SourceIDs = append([]string(nil), segment.SourceIDs...)
		s.pendingActivity = append(s.pendingActivity, segment)
	}
	if overflow := len(s.pendingActivity) - maxPendingActivitySegments; overflow > 0 {
		for i := 0; i < overflow; i++ {
			clear(s.pendingActivity[i].SourceIDs)
			s.pendingActivity[i] = activity.Segment{}
		}
		copy(s.pendingActivity, s.pendingActivity[overflow:])
		clear(s.pendingActivity[len(s.pendingActivity)-overflow:])
		s.pendingActivity = s.pendingActivity[:len(s.pendingActivity)-overflow]
		s.logger.Error("activity retry queue reached limit", "operation", "activity", "result", "oldest_segments_dropped", "dropped", overflow)
	}
}

func (s *Service) flushPendingActivity(ctx context.Context) error {
	if s == nil || s.activitySink == nil || ctx == nil {
		return ErrInvalidService
	}
	for len(s.pendingActivity) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.activitySink.PutActivitySegment(ctx, s.pendingActivity[0]); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if isPermanentActivityWriteError(err) {
				// A segment the store will never accept must not pin the queue.
				s.logger.Error("encrypted activity write rejected", "operation", "activity", "result", safeActivityWriteResult(err), "dropped", 1)
			} else {
				s.logger.Error("encrypted activity write failed", "operation", "activity", "result", safeActivityWriteResult(err), "retry_pending", len(s.pendingActivity))
				return err
			}
		}
		clear(s.pendingActivity[0].SourceIDs)
		s.pendingActivity[0] = activity.Segment{}
		copy(s.pendingActivity, s.pendingActivity[1:])
		s.pendingActivity[len(s.pendingActivity)-1] = activity.Segment{}
		s.pendingActivity = s.pendingActivity[:len(s.pendingActivity)-1]
	}
	return nil
}

func (s *Service) captureCurrent(ctx context.Context) error {
	if s.onCaptureCycle != nil {
		defer s.onCaptureCycle()
	}
	state, attemptedAt := HealthUnavailable, time.Now().UTC()
	var readIssue capture.ReadIssue
	partial := false
	defer func() {
		if state == HealthSaved || state == HealthUnchanged {
			s.health.LastReadablePartial = partial
		}
		s.reportHealth(state, attemptedAt, readIssue)
	}()
	handled, documentState, documentPartial, err := s.captureDocumentCurrent(ctx)
	if handled && err == nil {
		state, partial = documentState, documentPartial
		return nil
	}
	if handled && errors.Is(err, ErrStoreWrite) {
		if s.rejectPolicyCommit(ctx, err, &state) {
			return nil
		}
		state = HealthStorageError
		return ErrStoreWrite
	}
	var observation capture.SanitizedObservation
	if !handled {
		observation, err = s.capture.Capture(ctx)
	}
	if err != nil {
		readIssue = capture.ReadIssueForError(err)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var captureErr *capture.CaptureError
		if errors.As(err, &captureErr) {
			state = healthForCode(captureErr.Code)
			if readIssue.Valid() {
				if readIssue.Status == -25211 {
					state = HealthPermission
				} else if readIssue.Status == -25204 && state == HealthPermission {
					state = HealthUnavailable
				}
			}
			code, recoverable := safeCaptureCode(captureErr.Code)
			if !recoverable {
				s.logger.Error("capture configuration rejected", "operation", "capture", "result", code)
				return ErrCaptureFailed
			}
			s.pauseActivity(ctx)
			s.skipped.Add(1)
			attrs := []any{"operation", "capture", "result", code}
			if readIssue.Valid() {
				attrs = append(attrs, "read_stage", string(readIssue.Stage))
				if readIssue.Status != 0 {
					attrs = append(attrs, "ax_status", readIssue.Status)
				}
			}
			s.logger.Debug("capture frame skipped", attrs...)
			return nil
		}
		s.logger.Warn("capture operation failed", "operation", "capture", "result", "failed")
		return ErrCaptureFailed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := observation.Validate(); err != nil {
		s.logger.Warn("capture output rejected", "operation", "capture", "result", "invalid")
		return ErrCaptureFailed
	}
	if strings.TrimSpace(observation.Text()) == "" {
		state = HealthEmpty
		s.skipped.Add(1)
		s.pauseActivity(ctx)
		return nil
	}
	if !capture.MeetsMinimumContent(observation.AppID(), observation.Text()) {
		// Too little content to store, but the foreground visit still counts
		// toward activity timing (without evidence links).
		state = HealthEmpty
		s.droppedShort.Add(1)
		s.recordShortVisit(ctx, observation)
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	partial = observation.Truncated()
	contentID := observation.ContentID()
	newObservation := false
	observedAt := time.Now()
	capturedAt := observedAt.UTC()
	if contentID == s.lastContentID {
		s.duplicates.Add(1)
		state = HealthUnchanged
	} else {
		var putErr error
		if timestamped, ok := s.sink.(interface {
			PutObservationAt(context.Context, capture.SanitizedObservation, time.Time) error
		}); ok {
			putErr = timestamped.PutObservationAt(ctx, observation, capturedAt)
		} else {
			putErr = s.sink.PutObservation(ctx, observation)
		}
		if putErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if s.rejectPolicyCommit(ctx, putErr, &state) {
				return nil
			}
			var storeErr *storage.StoreError
			if errors.As(putErr, &storeErr) && storeErr.Code == storage.Duplicate {
				s.duplicates.Add(1)
				state = HealthUnchanged
			} else {
				state = HealthStorageError
				s.logger.Error("encrypted observation write failed", "operation", "capture", "result", "failed")
				return ErrStoreWrite
			}
		} else {
			s.persisted.Add(1)
			state = HealthSaved
			newObservation = true
		}
		s.lastContentID = contentID
	}
	sightingSaved := false
	if !newObservation {
		if recorder, ok := s.sink.(interface {
			RecordObservationSighting(context.Context, capture.SanitizedObservation, time.Time) error
		}); ok {
			if err := recorder.RecordObservationSighting(ctx, observation, capturedAt); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if s.rejectPolicyCommit(ctx, err, &state) {
					return nil
				}
				state = HealthStorageError
				s.logger.Error("encrypted visit write failed", "operation", "capture", "result", "failed")
				return ErrStoreWrite
			}
			sightingSaved = true
		}
	}
	if s.activity != nil {
		monotonic := observedAt.Sub(s.activityEpoch).Nanoseconds()
		segments, err := s.activity.Apply(activity.Event{Kind: activity.Seen, Monotonic: monotonic, Wall: capturedAt, SourceID: contentID, AppID: observation.AppID(), Domain: observation.Domain()})
		if err != nil {
			s.logger.Error("activity event rejected", "operation", "activity", "result", "invalid")
		} else {
			s.enqueueActivity(segments)
			if err := s.flushPendingActivity(ctx); err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	if (newObservation || sightingSaved) && s.onChange != nil {
		s.onChange()
	}
	if newObservation && s.onObservation != nil {
		s.onObservation(observation.Clone(), capturedAt)
	}
	return nil
}

func (s *Service) pauseActivity(ctx context.Context) {
	if s.activity == nil || ctx.Err() != nil {
		return
	}
	now := time.Now()
	segments, err := s.activity.Apply(activity.Event{Kind: activity.Idle, Monotonic: now.Sub(s.activityEpoch).Nanoseconds(), Wall: now.UTC()})
	if err != nil {
		return
	}
	s.enqueueActivity(segments)
	_ = s.flushPendingActivity(ctx)
}

// isPermanentActivityWriteError reports store rejections that retrying cannot fix.
func isPermanentActivityWriteError(err error) bool {
	var storeErr *storage.StoreError
	return errors.As(err, &storeErr) && (storeErr.Code == storage.Invalid || storeErr.Code == storage.Duplicate)
}

// safeActivityWriteResult records only a bounded error category, never backend
// error text that could contain local paths or data-derived values.
func safeActivityWriteResult(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var classified storage.SafeFailureClassifier
	if errors.As(err, &classified) && classified != nil {
		switch classified.SafeFailureCode() {
		case string(storage.Invalid), string(storage.Duplicate), string(storage.Closed), string(storage.Incomplete), "corrupt", "transaction":
			return "storage_" + classified.SafeFailureCode()
		}
	}
	return "storage_backend_error"
}

func safeCaptureCode(code capture.ErrorCode) (string, bool) {
	switch code {
	case capture.Unavailable, capture.ProbeFailed, capture.InvalidProbe, capture.Excluded,
		capture.ReadFailed, capture.LimitExceeded, capture.Cancelled, capture.TimedOut,
		capture.PermissionRequired, capture.ScreenPermissionRequired, capture.ScreenTextUnavailable, capture.ScreenNotVisible, capture.BrowserUnavailable, capture.DomainUnknown, capture.NoContent:
		return string(code), true
	case capture.UserIdle:
		return string(code), true
	default:
		return "failed", false
	}
}

func (s *Service) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	return Stats{Persisted: s.persisted.Load(), Duplicates: s.duplicates.Load(), Skipped: s.skipped.Load(), DroppedShort: s.droppedShort.Load()}
}

// A preference change may reject the frame between capture and its encrypted
// write. This is an expected privacy exclusion, never a storage outage.
func (s *Service) rejectPolicyCommit(ctx context.Context, err error, state *HealthState) bool {
	var rejected *capture.CaptureError
	if !errors.As(err, &rejected) || rejected.Code != capture.Excluded {
		return false
	}
	*state = HealthExcluded
	s.skipped.Add(1)
	s.pauseActivity(ctx)
	return true
}

func (s *Service) recordShortVisit(ctx context.Context, observation capture.SanitizedObservation) {
	if s.activity == nil || ctx.Err() != nil {
		return
	}
	now := time.Now().UTC()
	segments, err := s.activity.Apply(activity.Event{Kind: activity.Seen, Monotonic: now.Sub(s.activityEpoch).Nanoseconds(), Wall: now, AppID: observation.AppID(), Domain: observation.Domain()})
	if err != nil {
		s.logger.Error("activity event rejected", "operation", "activity", "result", "invalid")
		return
	}
	s.enqueueActivity(segments)
	_ = s.flushPendingActivity(ctx)
}
