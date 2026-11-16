package ingest

import (
	"context"
	"errors"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
)

type DocumentCapturer interface {
	OpenDocument(context.Context, time.Time) (capture.DocumentJob, error)
}
type DocumentPublication struct {
	Committed, New bool
	// SourceID is the canonical first-part identity, from the committed durable
	// receipt. On duplicate publication it must use the original publication.
	SourceID string
}
type DocumentSink interface {
	EncryptedAtRest() bool
	BeginCapture(context.Context, capture.CaptureHeader) error
	AppendCaptureParts(context.Context, capture.CaptureHeader, []capture.CapturePart) error
	PublishDocumentCapture(context.Context, capture.CaptureSummary) (DocumentPublication, error)
	DiscardCaptureStage(context.Context, string) error
}

func (s *Service) SetDocumentNotify(notify func(capture.CaptureSummary, bool)) {
	if s == nil || s.started.Load() {
		return
	}
	s.onDocument = notify
}

func (s *Service) closeDocument() {
	if s.document == nil {
		return
	}
	job := s.document
	s.document = nil
	id := job.Header().CaptureID()
	job.Close()
	if sink, ok := s.sink.(DocumentSink); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// Failed cleanup remains encrypted and private; durable startup recovery
		// drains unpublished stages before permitting ordinary reads.
		if err := sink.DiscardCaptureStage(ctx, id); err != nil {
			s.logger.Warn("capture stage cleanup deferred", "operation", "capture", "result", "cleanup_pending")
		}
	}
}

// One round per event-loop turn. New focus, pause, policy changes and idle state
// are serviced between rounds instead of letting a large document monopolize
// the capture goroutine. No part is visible before the checked final summary.
func (s *Service) captureDocumentCurrent(ctx context.Context) (bool, HealthState, bool, error) {
	capturer, capable := s.capture.(DocumentCapturer)
	sink, writable := s.sink.(DocumentSink)
	if !capable || !writable {
		return false, "", false, nil
	}
	if s.document == nil {
		job, err := capturer.OpenDocument(ctx, time.Now().UTC())
		if errors.Is(err, capture.ErrDocumentUnsupported) {
			return false, "", false, nil
		}
		if err != nil {
			return true, "", false, err
		}
		if job == nil || job.Header().ValidateForAdmission() != nil {
			if job != nil {
				job.Close()
			}
			return true, "", false, ErrCaptureFailed
		}
		s.document = job
		if err := sink.BeginCapture(ctx, job.Header()); err != nil {
			s.closeDocument()
			return true, "", false, documentWriteError(err)
		}
	}
	job := s.document
	var writeErr error
	progress, err := job.Read(ctx, func(round context.Context, parts []capture.CapturePart) error {
		writeErr = sink.AppendCaptureParts(round, job.Header(), parts)
		return writeErr
	})
	if err != nil {
		s.closeDocument()
		if writeErr != nil {
			return true, "", false, documentWriteError(writeErr)
		}
		if capture.DocumentFallbackAllowed(err) {
			return false, "", false, nil
		}
		return true, "", false, err
	}
	if !progress.Done {
		return true, HealthReading, progress.StructureLimited, nil
	}
	summary := progress.Summary
	if summary.ValidateForAdmission() != nil || summary.Header().CaptureID() != job.Header().CaptureID() {
		s.closeDocument()
		return true, "", false, ErrCaptureFailed
	}
	receipt, err := sink.PublishDocumentCapture(ctx, summary)
	if err != nil && !receipt.Committed {
		s.closeDocument()
		return true, "", false, documentWriteError(err)
	}
	if !receipt.Committed || receipt.SourceID != "" && !validDocumentSourceID(receipt.SourceID) {
		s.closeDocument()
		return true, "", false, ErrStoreWrite
	}
	// Publication admission has returned. Cleanup uses its own bounded context,
	// outside the preference lock, and cannot suppress the committed callback.
	// Canonical owners have no stage, so cleanup is a harmless no-op there.
	defer s.closeDocument()
	if err != nil {
		s.logger.Warn("capture duplicate cleanup deferred", "operation", "capture", "result", "cleanup_pending")
	}
	state := HealthUnchanged
	if receipt.New {
		s.persisted.Add(1)
		state = HealthSaved
	} else {
		s.duplicates.Add(1)
	}
	now := time.Now()
	if s.activity != nil && receipt.SourceID != "" {
		segments, err := s.activity.Apply(activity.Event{Kind: activity.Seen, Monotonic: now.Sub(s.activityEpoch).Nanoseconds(), Wall: now.UTC(), SourceID: receipt.SourceID, AppID: summary.Header().AppID(), Domain: summary.Header().Domain()})
		if err == nil {
			s.enqueueActivity(segments)
			if err := s.flushPendingActivity(ctx); err != nil && ctx.Err() != nil {
				return true, state, false, ctx.Err()
			}
		}
	}
	if s.onChange != nil {
		s.onChange()
	}
	if s.onDocument != nil {
		s.onDocument(summary, receipt.New)
	}
	return true, state, summary.Coverage() != capture.CaptureComplete, nil
}
func validDocumentSourceID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
func documentWriteError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.Join(ErrStoreWrite, err)
}
