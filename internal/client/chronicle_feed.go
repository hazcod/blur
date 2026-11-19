package client

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/storage"
)

const maxChronicleFeedQueue = 32

type capturedEvidence struct {
	evidence   chronicle.Evidence
	at         time.Time
	generation uint64
}

// chronicleFeed is an event-fed, memory-bounded bridge. It retains only
// explicitly opted-in sanitized evidence until the next observed hour; it
// neither polls nor reads other applications or stored history.
type chronicleFeed struct {
	mu      sync.Mutex
	worker  *chronicle.Worker
	current *chronicle.Hour
	// A conservative time range preserves queue-overflow coverage even before
	// the first queued event has been accepted, using fixed memory.
	limitedStart      time.Time
	limitedEnd        time.Time
	queue             chan capturedEvidence
	timer             *time.Timer
	ctx               context.Context
	cancel            context.CancelFunc
	done              chan struct{}
	generation        uint64
	captures          []chronicleCaptureJob
	captureWake       chan struct{}
	captureReadCancel context.CancelFunc
}

func newChronicleFeed() *chronicleFeed {
	ctx, cancel := context.WithCancel(context.Background())
	f := &chronicleFeed{queue: make(chan capturedEvidence, maxChronicleFeedQueue), ctx: ctx, cancel: cancel, done: make(chan struct{}), generation: 1, captureWake: make(chan struct{}, 1)}
	go f.run()
	return f
}

// Purge discards all pending and in-progress evidence. It runs at the start of
// every memory deletion window so deleted sources are never summarized later.
func (f *chronicleFeed) Purge() {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.current = nil
	f.cancelCapturesLocked()
	f.limitedStart, f.limitedEnd = time.Time{}, time.Time{}
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
	f.drainQueueLocked()
	f.mu.Unlock()
}

// CurrentHour returns a deep copy of the in-progress hour's evidence, or false
// when nothing is being collected (no worker, purged, or paused). The copy is
// bounded by chronicle.MaxEvidence and shares no memory with the feed.
func (f *chronicleFeed) CurrentHour() (chronicle.Hour, bool) {
	if f == nil {
		return chronicle.Hour{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current == nil || f.worker == nil {
		return chronicle.Hour{}, false
	}
	n := min(len(f.current.Evidence), chronicle.MaxEvidence)
	hour := chronicle.Hour{Start: f.current.Start, End: f.current.End, Evidence: make([]chronicle.Evidence, n), SourceLimitReached: f.current.SourceLimitReached || f.capturePendingForHourLocked(f.current.Start, f.current.End)}
	for i := 0; i < n; i++ {
		item := f.current.Evidence[i]
		hour.Evidence[i] = item.Clone()
	}
	return hour, true
}

func (f *chronicleFeed) drainQueueLocked() {
	for {
		select {
		case <-f.queue:
		default:
			return
		}
	}
}

func (f *chronicleFeed) SetWorker(w *chronicle.Worker) {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.worker != w {
		f.cancelCapturesLocked()
		f.current = nil
		f.limitedStart, f.limitedEnd = time.Time{}, time.Time{}
		if f.timer != nil {
			f.timer.Stop()
			f.timer = nil
		}
		f.drainQueueLocked()
	}
	f.worker = w
	f.mu.Unlock()
}
func (f *chronicleFeed) Add(observation capture.SanitizedObservation, at time.Time) {
	if f == nil || at.IsZero() {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.worker == nil {
		return
	}
	evidence, err := chronicle.NewEvidence(observation, at.UTC(), chronicleSalience(observation))
	if err != nil {
		return
	}
	select {
	case f.queue <- capturedEvidence{evidence: evidence, at: at.UTC(), generation: f.generation}:
	default:
		start := at.UTC().Truncate(time.Hour)
		if f.limitedStart.IsZero() || start.Before(f.limitedStart) {
			f.limitedStart = start
		}
		if end := start.Add(time.Hour); end.After(f.limitedEnd) {
			f.limitedEnd = end
		}
		if f.current != nil && !at.Before(f.current.Start) && at.Before(f.current.End) {
			f.current.SourceLimitReached = true
		}
		slog.Debug("chronicle evidence skipped", "result", "bounded_queue_full")
	}
}
func (f *chronicleFeed) run() {
	defer close(f.done)
	for {
		select {
		case <-f.ctx.Done():
			return
		case item := <-f.queue:
			f.accept(item)
		case <-f.captureWake:
			f.processCapture()
		}
	}
}
func (f *chronicleFeed) accept(item capturedEvidence) {
	at := item.at.UTC()
	start := at.Truncate(time.Hour)
	f.mu.Lock()
	worker := f.worker
	if item.generation != 0 && item.generation != f.generation {
		f.mu.Unlock()
		return
	}
	if worker == nil {
		f.current = nil
		f.mu.Unlock()
		return
	}
	if f.current == nil {
		f.current = &chronicle.Hour{Start: start, End: start.Add(time.Hour), SourceLimitReached: f.sourceLimitReachedLocked(start)}
		f.appendEvidence(item)
		f.startBoundaryTimerLocked()
		f.mu.Unlock()
		return
	}
	current := f.current
	if at.Before(current.Start) {
		f.mu.Unlock()
		return
	}
	if !at.Before(current.End) {
		closed := *current
		closed.SourceLimitReached = closed.SourceLimitReached || f.capturePendingForHourLocked(closed.Start, closed.End)
		if f.timer != nil {
			f.timer.Stop()
			f.timer = nil
		}
		f.current = &chronicle.Hour{Start: start, End: start.Add(time.Hour), SourceLimitReached: f.sourceLimitReachedLocked(start)}
		f.appendEvidence(item)
		f.startBoundaryTimerLocked()
		f.mu.Unlock()
		f.schedule(worker, closed)
		return
	}
	f.appendEvidence(item)
	f.mu.Unlock()
}

func (f *chronicleFeed) sourceLimitReachedLocked(start time.Time) bool {
	if !f.limitedEnd.IsZero() && !start.Before(f.limitedEnd) {
		f.limitedStart, f.limitedEnd = time.Time{}, time.Time{}
		return false
	}
	return !f.limitedStart.IsZero() && !start.Before(f.limitedStart) && start.Before(f.limitedEnd)
}

func (f *chronicleFeed) startBoundaryTimerLocked() {
	if f.current == nil {
		return
	}
	wait := time.Until(f.current.End)
	if wait < 0 {
		wait = 0
	}
	f.timer = time.AfterFunc(wait, func() { f.closeCurrent(time.Now().UTC()) })
}

func (f *chronicleFeed) closeCurrent(now time.Time) {
	f.mu.Lock()
	if f.current == nil || now.Before(f.current.End) {
		f.mu.Unlock()
		return
	}
	hour := *f.current
	hour.SourceLimitReached = hour.SourceLimitReached || f.capturePendingForHourLocked(hour.Start, hour.End)
	worker := f.worker
	f.current = nil
	f.timer = nil
	f.mu.Unlock()
	f.schedule(worker, hour)
}

func (f *chronicleFeed) schedule(worker *chronicle.Worker, hour chronicle.Hour) {
	if worker == nil || len(hour.Evidence) == 0 {
		return
	}
	if err := worker.Schedule(f.ctx, hour); err != nil {
		slog.Debug("closed chronicle hour skipped", "result", "not_scheduled")
	}
}
func (f *chronicleFeed) appendEvidence(item capturedEvidence) {
	if f.current == nil {
		return
	}
	for _, existing := range f.current.Evidence {
		if existing.SourceID() == item.evidence.SourceID() {
			return
		}
	}
	if len(f.current.Evidence) >= chronicle.MaxEvidence {
		f.current.SourceLimitReached = true
		return
	}
	f.current.Evidence = append(f.current.Evidence, item.evidence)
}
func chronicleSalience(observation capture.SanitizedObservation) uint8 {
	value := len(observation.Text()) / 128
	if value > 8 {
		value = 8
	}
	return uint8(1 + value)
}
func (f *chronicleFeed) Close() {
	if f == nil {
		return
	}
	f.cancel()
	<-f.done
	f.mu.Lock()
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
	f.current = nil
	f.limitedStart, f.limitedEnd = time.Time{}, time.Time{}
	f.worker = nil
	f.cancelCapturesLocked()
	f.mu.Unlock()
}

// sourceVerifiedRepository re-checks, immediately before persisting, that every
// source a summary cites still exists, and holds the memory-mutation fence
// through the write. Summaries citing deleted sources, or generated while a
// deletion window opened, are dropped.
type sourceVerifiedRepository struct {
	inner   chronicle.SummaryRepository
	sources interface {
		GetObservation(context.Context, string) (capture.SanitizedObservation, bool, error)
	}
	client *Client
}

func (r sourceVerifiedRepository) GetChronicle(ctx context.Context, id string) (chronicle.Summary, bool, error) {
	return r.inner.GetChronicle(ctx, id)
}

func (r sourceVerifiedRepository) PutChronicle(ctx context.Context, summary chronicle.Summary) error {
	epoch, stable := r.client.BeginMemoryRead()
	if !stable {
		return ErrMemoryChanging
	}
	for _, id := range summary.DependencyIDs() {
		_, found, err := storage.ReadSourceEvidence(ctx, r.sources, id)
		if err != nil {
			return err
		}
		if !found {
			slog.Debug("chronicle dropped", "result", "source_gone")
			return ErrMemoryChanging
		}
	}
	var err error
	if !r.client.PublishMemoryRead(epoch, func() { err = r.inner.PutChronicle(ctx, summary) }) {
		return ErrMemoryChanging
	}
	return err
}

// Rollup access is an optional storage capability. Reads pass straight through;
// a rollup write is fenced exactly like an hourly recap write.
func (r sourceVerifiedRepository) rollups() (chronicle.RollupRepository, bool) {
	inner, ok := r.inner.(chronicle.RollupRepository)
	return inner, ok
}

func (r sourceVerifiedRepository) ListChronicles(ctx context.Context, start, end time.Time, after string, limit int) ([]chronicle.Summary, string, error) {
	inner, ok := r.rollups()
	if !ok {
		return nil, "", chronicle.ErrWorkerUnavailable
	}
	return inner.ListChronicles(ctx, start, end, after, limit)
}

func (r sourceVerifiedRepository) GetChronicleRollup(ctx context.Context, id string) (chronicle.Rollup, bool, error) {
	inner, ok := r.rollups()
	if !ok {
		return chronicle.Rollup{}, false, chronicle.ErrWorkerUnavailable
	}
	return inner.GetChronicleRollup(ctx, id)
}

func (r sourceVerifiedRepository) ListChronicleRollups(ctx context.Context, level string, start, end time.Time, after string, limit int) ([]chronicle.Rollup, string, error) {
	inner, ok := r.rollups()
	if !ok {
		return nil, "", chronicle.ErrWorkerUnavailable
	}
	return inner.ListChronicleRollups(ctx, level, start, end, after, limit)
}

func (r sourceVerifiedRepository) MarkChronicleRollupDirty(ctx context.Context, id, zone string) error {
	inner, ok := r.rollups()
	if !ok {
		return chronicle.ErrWorkerUnavailable
	}
	return inner.MarkChronicleRollupDirty(ctx, id, zone)
}

func (r sourceVerifiedRepository) PutChronicleRollup(ctx context.Context, rollup chronicle.Rollup) error {
	inner, ok := r.rollups()
	if !ok {
		return chronicle.ErrWorkerUnavailable
	}
	epoch, stable := r.client.BeginMemoryRead()
	if !stable {
		return ErrMemoryChanging
	}
	for _, id := range rollup.SourceIDs {
		_, found, err := storage.ReadSourceEvidence(ctx, r.sources, id)
		if err != nil {
			return err
		}
		if !found {
			slog.Debug("chronicle rollup dropped", "result", "source_gone")
			return ErrMemoryChanging
		}
	}
	var err error
	if !r.client.PublishMemoryRead(epoch, func() { err = inner.PutChronicleRollup(ctx, rollup) }) {
		return ErrMemoryChanging
	}
	return err
}

// GetObservationWithCaptureTime preserves the actual encrypted source-reader
// capability through the production repository adapter. Capture time is the
// saved-first clock, independent of the hour evidence's possible visit time.
func (r sourceVerifiedRepository) GetObservationWithCaptureTime(ctx context.Context, id string) (capture.SanitizedObservation, time.Time, bool, error) {
	var empty capture.SanitizedObservation
	if ctx == nil || r.client == nil {
		return empty, time.Time{}, false, chronicle.ErrWorkerUnavailable
	}
	epoch, stable := r.client.BeginMemoryRead()
	if !stable {
		return empty, time.Time{}, false, ErrMemoryChanging
	}
	reader, ok := r.sources.(interface {
		GetObservationWithCaptureTime(context.Context, string) (capture.SanitizedObservation, time.Time, bool, error)
	})
	if !ok {
		return empty, time.Time{}, false, chronicle.ErrWorkerUnavailable
	}
	source, at, found, err := reader.GetObservationWithCaptureTime(ctx, id)
	if err != nil {
		return empty, time.Time{}, false, err
	}
	if !r.client.MemoryReadCurrent(epoch) {
		return empty, time.Time{}, false, ErrMemoryChanging
	}
	return source, at, found, nil
}

// GetSourceEvidenceWithCaptureTime preserves multipart authority through the
// same memory-epoch fence as legacy observations.
func (r sourceVerifiedRepository) GetSourceEvidenceWithCaptureTime(ctx context.Context, id string) (capture.Evidence, time.Time, bool, error) {
	if ctx == nil || r.client == nil {
		return capture.Evidence{}, time.Time{}, false, chronicle.ErrWorkerUnavailable
	}
	epoch, stable := r.client.BeginMemoryRead()
	if !stable {
		return capture.Evidence{}, time.Time{}, false, ErrMemoryChanging
	}
	reader, ok := r.sources.(storage.ObservationTimeReader)
	if !ok {
		return capture.Evidence{}, time.Time{}, false, chronicle.ErrWorkerUnavailable
	}
	source, at, found, err := storage.ReadSourceEvidenceWithCaptureTime(ctx, reader, id)
	if err != nil {
		return capture.Evidence{}, time.Time{}, false, err
	}
	if !r.client.MemoryReadCurrent(epoch) {
		return capture.Evidence{}, time.Time{}, false, ErrMemoryChanging
	}
	return source, at, found, nil
}
