package client

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
)

type chronicleCaptureReader interface {
	CaptureIndexPending(context.Context) (bool, error)
	ListCaptureSourceIDs(context.Context, string, int64, int) ([]string, bool, error)
	GetSourceEvidenceWithCaptureTime(context.Context, string) (capture.Evidence, time.Time, bool, error)
}
type chronicleCaptureJob struct {
	edition string
	at      time.Time
	after   int64
	reader  chronicleCaptureReader
}

// AddCapture queues only bounded source-free publication metadata. Both new
// editions and unchanged-source revisits retain the supplied real capture clock.
// Index readiness wakes the existing joined feed worker; it creates no goroutine.
func (f *chronicleFeed) AddCapture(summary capture.CaptureSummary, _ bool, sources any) {
	if f == nil || summary.Validate() != nil {
		return
	}
	reader, ok := sources.(chronicleCaptureReader)
	if !ok {
		return
	}
	at := summary.Header().CapturedAt().UTC()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.worker == nil || f.ctx.Err() != nil {
		return
	}
	for _, job := range f.captures {
		if job.edition == summary.ContentID() && job.at.Equal(at) {
			return
		}
	}
	if len(f.captures) >= maxChronicleFeedQueue {
		f.markLimitedLocked(at)
		slog.Debug("chronicle capture skipped", "result", "bounded_queue_full")
		return
	}
	f.captures = append(f.captures, chronicleCaptureJob{summary.ContentID(), at, -1, reader})
	f.wakeCapturesLocked()
}

func (f *chronicleFeed) WakeCaptures() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ctx.Err() == nil {
		f.wakeCapturesLocked()
	}
}
func (f *chronicleFeed) wakeCapturesLocked() {
	select {
	case f.captureWake <- struct{}{}:
	default:
	}
}
func (f *chronicleFeed) cancelCapturesLocked() {
	f.generation++
	if f.captureReadCancel != nil {
		f.captureReadCancel()
		f.captureReadCancel = nil
	}
	clear(f.captures)
	f.captures = nil
}
func (f *chronicleFeed) markLimitedLocked(at time.Time) {
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
}

func (f *chronicleFeed) capturePendingForHourLocked(start, end time.Time) bool {
	for _, job := range f.captures {
		if !job.at.Before(start) && job.at.Before(end) {
			return true
		}
	}
	return false
}

// AddSourceEvidence projects a real immutable source before retaining it. A
// large part contributes labelled head/tail context, never an invented sourceID.
func (f *chronicleFeed) AddSourceEvidence(source capture.Evidence, at time.Time) {
	if f == nil || at.IsZero() {
		return
	}
	e, err := chronicle.NewSourceEvidence(source, at.UTC(), uint8(min(9, 1+len(source.Text())/128)))
	if err != nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.worker == nil || f.ctx.Err() != nil {
		return
	}
	select {
	case f.queue <- capturedEvidence{e, at.UTC(), f.generation}:
	default:
		f.markLimitedLocked(at)
	}
}

func (f *chronicleFeed) processCapture() {
	f.mu.Lock()
	if len(f.captures) == 0 || f.worker == nil || f.ctx.Err() != nil {
		f.mu.Unlock()
		return
	}
	job, generation := f.captures[0], f.generation
	ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
	f.captureReadCancel = cancel
	f.mu.Unlock()
	defer cancel()
	pending, err := job.reader.CaptureIndexPending(ctx)
	if err == nil && !pending && !time.Now().UTC().Before(job.at.Truncate(time.Hour).Add(time.Hour)) {
		f.recoverCaptureHour(ctx, job, generation)
		return
	}
	var ids []string
	var more, found bool
	var source capture.Evidence
	if err == nil && !pending {
		ids, more, err = job.reader.ListCaptureSourceIDs(ctx, job.edition, job.after, 1)
		if err == nil && len(ids) == 1 {
			source, _, found, err = job.reader.GetSourceEvidenceWithCaptureTime(ctx, ids[0])
		}
	}
	var projected chronicle.Evidence
	if err == nil && found {
		projected, err = chronicle.NewSourceEvidence(source, job.at, uint8(min(9, 1+len(source.Text())/128)))
		if err == nil && (len(ids) != 1 || source.SourceID() != ids[0]) {
			err = chronicle.ErrInvalid
		}
	}
	f.mu.Lock()
	if f.generation != generation || f.ctx.Err() != nil {
		f.mu.Unlock()
		return
	}
	f.captureReadCancel = nil
	if pending && err == nil {
		f.mu.Unlock()
		return
	} // durable index callback will wake us
	if err != nil || !found || len(ids) != 1 {
		f.markLimitedLocked(job.at)
		f.captures[0] = chronicleCaptureJob{}
		f.captures = f.captures[1:]
		f.wakeCapturesLocked()
		f.mu.Unlock()
		slog.Debug("chronicle capture skipped", "result", "source_unavailable")
		return
	}
	if more && job.after+2 < chronicle.MaxEvidence {
		f.captures[0].after++
	} else {
		if more {
			f.markLimitedLocked(job.at)
		}
		f.captures[0] = chronicleCaptureJob{}
		f.captures = f.captures[1:]
	}
	f.wakeCapturesLocked()
	f.mu.Unlock()
	f.accept(capturedEvidence{projected, job.at, generation})
}

func (f *chronicleFeed) recoverCaptureHour(ctx context.Context, job chronicleCaptureJob, generation uint64) {
	reader, ok := job.reader.(interface {
		CaptureHourEvidence(context.Context, time.Time) (chronicle.Hour, error)
	})
	var hour chronicle.Hour
	var err error
	if ok {
		hour, err = reader.CaptureHourEvidence(ctx, job.at.Truncate(time.Hour))
	}
	f.mu.Lock()
	if f.generation != generation || f.ctx.Err() != nil {
		f.mu.Unlock()
		return
	}
	worker := f.worker
	f.mu.Unlock()
	if err == nil && ok && worker != nil && len(hour.Evidence) > 0 {
		err = worker.Schedule(ctx, hour)
	}
	f.mu.Lock()
	if f.generation != generation || f.ctx.Err() != nil {
		f.mu.Unlock()
		return
	}
	if errors.Is(err, chronicle.ErrWorkerDuplicate) || errors.Is(err, chronicle.ErrWorkerBusy) {
		// A prior generation of this hour is still running. Retain the same
		// source-free job; the later full-hour fingerprint must get a turn.
		f.mu.Unlock()
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
			f.WakeCaptures()
		}
		return
	}
	f.captureReadCancel = nil
	f.captures[0] = chronicleCaptureJob{}
	f.captures = f.captures[1:]
	if err != nil || !ok {
		f.markLimitedLocked(job.at)
	}
	f.wakeCapturesLocked()
	f.mu.Unlock()
	if err != nil || !ok {
		slog.Debug("closed chronicle capture skipped", "result", "source_unavailable")
	}
}
