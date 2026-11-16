package client

import (
	"context"
	"errors"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/ingest"
	"github.com/hazcod/blur/internal/preferences"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

type nativeCaptureStore interface {
	ingest.ObservationSink
	ingest.ActivitySink
	PutObservationAt(context.Context, capture.SanitizedObservation, time.Time) error
	RecordObservationSighting(context.Context, capture.SanitizedObservation, time.Time) error
}

// Linearizes native capture admission and each encrypted write with preferences
// Save, including duplicate visits and pending activity. No raw text is handled.
type nativeCaptureCommitSink struct {
	store       nativeCaptureStore
	preferences *preferences.Store
}

type nativeDocumentStore interface {
	BeginCapture(context.Context, capture.CaptureHeader) error
	AppendCaptureParts(context.Context, capture.CaptureHeader, []capture.CapturePart) error
	PublishCaptureWithSourceReceipt(context.Context, capture.CaptureSummary) (durable.CapturePublicationReceipt, error)
	DiscardCaptureStage(context.Context, string) error
}

func (s *nativeCaptureCommitSink) BeginCapture(ctx context.Context, header capture.CaptureHeader) error {
	return s.admit(ctx, header.AppID(), header.Domain(), func(ctx context.Context) error {
		store, ok := s.store.(nativeDocumentStore)
		if !ok {
			return ErrNotConfigured
		}
		return store.BeginCapture(ctx, header)
	})
}
func (s *nativeCaptureCommitSink) AppendCaptureParts(ctx context.Context, header capture.CaptureHeader, parts []capture.CapturePart) error {
	return s.admit(ctx, header.AppID(), header.Domain(), func(ctx context.Context) error {
		store, ok := s.store.(nativeDocumentStore)
		if !ok {
			return ErrNotConfigured
		}
		return store.AppendCaptureParts(ctx, header, parts)
	})
}
func (s *nativeCaptureCommitSink) PublishDocumentCapture(ctx context.Context, summary capture.CaptureSummary) (receipt ingest.DocumentPublication, err error) {
	err = s.admit(ctx, summary.Header().AppID(), summary.Header().Domain(), func(ctx context.Context) error {
		store, ok := s.store.(nativeDocumentStore)
		if !ok {
			return ErrNotConfigured
		}
		committed, err := store.PublishCaptureWithSourceReceipt(ctx, summary)
		receipt = ingest.DocumentPublication{Committed: committed.Committed, New: committed.New, SourceID: committed.SourceID}
		return err
	})
	return receipt, err
}
func (s *nativeCaptureCommitSink) DiscardCaptureStage(ctx context.Context, captureID string) error {
	// Cleanup must also run after capture was paused or its target excluded.
	if s == nil || s.store == nil || ctx == nil {
		return ErrNotConfigured
	}
	store, ok := s.store.(nativeDocumentStore)
	if !ok {
		return ErrNotConfigured
	}
	cleanup, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return store.DiscardCaptureStage(cleanup, captureID)
}

func (s *nativeCaptureCommitSink) EncryptedAtRest() bool {
	return s != nil && s.store != nil && s.store.EncryptedAtRest()
}
func (s *nativeCaptureCommitSink) admit(ctx context.Context, app, domain string, write func(context.Context) error) error {
	if s == nil || s.store == nil || s.preferences == nil || ctx == nil {
		return &capture.CaptureError{Code: capture.Excluded}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err := s.preferences.WithConfig(ctx, func(config preferences.Config) error {
		if !config.CaptureEnabled || !capture.TargetAllowed(captureExclusionsFromPreferences(config), app, domain) {
			return &capture.CaptureError{Code: capture.Excluded}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return write(ctx)
	})
	return err
}
func (s *nativeCaptureCommitSink) PutObservation(ctx context.Context, o capture.SanitizedObservation) error {
	return s.admit(ctx, o.AppID(), o.Domain(), func(ctx context.Context) error { return s.store.PutObservation(ctx, o) })
}
func (s *nativeCaptureCommitSink) PutObservationAt(ctx context.Context, o capture.SanitizedObservation, at time.Time) error {
	return s.admit(ctx, o.AppID(), o.Domain(), func(ctx context.Context) error { return s.store.PutObservationAt(ctx, o, at) })
}
func (s *nativeCaptureCommitSink) RecordObservationSighting(ctx context.Context, o capture.SanitizedObservation, at time.Time) error {
	return s.admit(ctx, o.AppID(), o.Domain(), func(ctx context.Context) error { return s.store.RecordObservationSighting(ctx, o, at) })
}
func (s *nativeCaptureCommitSink) PutActivitySegment(ctx context.Context, segment activity.Segment) error {
	err := s.admit(ctx, segment.AppID, segment.Domain, func(ctx context.Context) error { return s.store.PutActivitySegment(ctx, segment) })
	var rejected *capture.CaptureError
	if errors.As(err, &rejected) && rejected.Code == capture.Excluded {
		// Permanent rejection drops pending activity rather than resurrecting it if
		// the app/domain becomes allowed again later.
		return &storage.StoreError{Code: storage.Invalid}
	}
	return err
}

// The final native acquisition runs under the same Save mutex as commits.
// A preference change during AX verification is checked before any pixels;
// once Save returns no capture admitted under the old policy can still begin.
// Pause/deletion cancellation remains independent of this preference mutex.
func nativeCaptureVisualAdmission(store *preferences.Store) func(context.Context, string, string, func(context.Context) error) error {
	return func(ctx context.Context, app, domain string, read func(context.Context) error) error {
		if store == nil || ctx == nil || read == nil {
			return &capture.CaptureError{Code: capture.Excluded}
		}
		return store.WithConfig(ctx, func(config preferences.Config) error {
			if !config.CaptureEnabled || !capture.TargetAllowed(captureExclusionsFromPreferences(config), app, domain) {
				return &capture.CaptureError{Code: capture.Excluded}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			return read(ctx)
		})
	}
}
