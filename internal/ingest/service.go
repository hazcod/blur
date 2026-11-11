// Package ingest connects passive platform events to sanitized encrypted
// observations. It never handles raw accessibility frames or invokes AI.
package ingest

import (
	"context"
	"errors"
	"log/slog"
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

type Stats struct{ Persisted, Duplicates, Skipped uint64 }

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
	sink            ObservationSink
	logger          *slog.Logger
	activity        *activity.Engine
	activitySink    ActivitySink
	activityEpoch   time.Time
	pendingActivity []activity.Segment
	onChange        func()
	onObservation   func(capture.SanitizedObservation, time.Time)
	onCaptureCycle  func()
	started         atomic.Bool
	lastContentID   string // Run is the sole writer; this stores a hash, never captured text.
	persisted       atomic.Uint64
	duplicates      atomic.Uint64
	skipped         atomic.Uint64
}

// SetCaptureCycleNotify registers a non-blocking callback after each
// event-driven capture attempt, including excluded apps. Configure it before Run.
func (s *Service) SetCaptureCycleNotify(notify func()) {
	if s == nil || s.started.Load() {
		return
	}
	s.onCaptureCycle = notify
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
		event, err := s.source.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.logger.Warn("capture event source stopped", "operation", "capture", "result", "unavailable")
			return ErrCaptureFailed
		}
		switch event.Kind {
		case "frontmost_app_changed", "focused_context_changed":
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
	observation, err := s.capture.Capture(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var captureErr *capture.CaptureError
		if errors.As(err, &captureErr) {
			code, recoverable := safeCaptureCode(captureErr.Code)
			if !recoverable {
				s.logger.Error("capture configuration rejected", "operation", "capture", "result", code)
				return ErrCaptureFailed
			}
			s.skipped.Add(1)
			s.logger.Debug("capture frame skipped", "operation", "capture", "result", code)
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
	if err := ctx.Err(); err != nil {
		return err
	}
	contentID := observation.ContentID()
	newObservation := false
	capturedAt := time.Now().UTC()
	if contentID == s.lastContentID {
		s.duplicates.Add(1)
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
			var storeErr *storage.StoreError
			if errors.As(putErr, &storeErr) && storeErr.Code == storage.Duplicate {
				s.duplicates.Add(1)
			} else {
				s.logger.Error("encrypted observation write failed", "operation", "capture", "result", "failed")
				return ErrStoreWrite
			}
		} else {
			s.persisted.Add(1)
			newObservation = true
		}
		s.lastContentID = contentID
	}
	if s.activity != nil {
		monotonic := capturedAt.Sub(s.activityEpoch).Nanoseconds()
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
	if newObservation && s.onChange != nil {
		s.onChange()
	}
	if newObservation && s.onObservation != nil {
		s.onObservation(observation.Clone(), capturedAt)
	}
	return nil
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
		capture.ReadFailed, capture.LimitExceeded, capture.Cancelled, capture.TimedOut:
		return string(code), true
	default:
		return "failed", false
	}
}

func (s *Service) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	return Stats{Persisted: s.persisted.Load(), Duplicates: s.duplicates.Load(), Skipped: s.skipped.Load()}
}
