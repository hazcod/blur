package client

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/browsercapture"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/ingest"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/preferences"
	"github.com/hazcod/blur/internal/storage"
)

// Receiver admission already holds the Safari-specific preference policy.
// Adapt raw document storage here without nesting another preference lock.
type safariCaptureSink struct {
	storage.EncryptedStore
	documents nativeDocumentStore
}

func (s *safariCaptureSink) BeginCapture(ctx context.Context, header capture.CaptureHeader) error {
	return s.documents.BeginCapture(ctx, header)
}
func (s *safariCaptureSink) AppendCaptureParts(ctx context.Context, header capture.CaptureHeader, parts []capture.CapturePart) error {
	return s.documents.AppendCaptureParts(ctx, header, parts)
}
func (s *safariCaptureSink) PublishDocumentCapture(ctx context.Context, summary capture.CaptureSummary) (ingest.DocumentPublication, error) {
	receipt, err := s.documents.PublishCaptureWithSourceReceipt(ctx, summary)
	return ingest.DocumentPublication{Committed: receipt.Committed, New: receipt.New, SourceID: receipt.SourceID}, err
}
func (s *safariCaptureSink) DiscardCaptureStage(ctx context.Context, id string) error {
	return s.documents.DiscardCaptureStage(ctx, id)
}
func (s *safariCaptureSink) PutObservationAt(ctx context.Context, observation capture.SanitizedObservation, at time.Time) error {
	if timed, ok := s.EncryptedStore.(interface {
		PutObservationAt(context.Context, capture.SanitizedObservation, time.Time) error
	}); ok {
		return timed.PutObservationAt(ctx, observation, at)
	}
	return s.EncryptedStore.PutObservation(ctx, observation)
}
func (s *safariCaptureSink) RecordObservationSighting(ctx context.Context, observation capture.SanitizedObservation, at time.Time) error {
	if visits, ok := s.EncryptedStore.(storage.ObservationSightings); ok {
		return visits.RecordObservationSighting(ctx, observation, at)
	}
	return ErrNotConfigured
}

func newSafariPageReceiver(grants *localapi.GrantStore, store storage.EncryptedStore, prefs *preferences.Store, enabled func() bool, clock func() time.Time,
	observation func(capture.SanitizedObservation, time.Time), document func(capture.CaptureSummary, bool)) (*browsercapture.Receiver, error) {
	documents, ok := store.(nativeDocumentStore)
	if !ok {
		return nil, ErrNotConfigured
	}
	sink := &safariCaptureSink{EncryptedStore: store, documents: documents}
	receiver, err := browsercapture.NewReceiver(grants, sink, enabled, clock, safariCapturePreferencePolicy(prefs))
	if err != nil {
		return nil, err
	}
	receiver.SetNotifications(observation, document)
	return receiver, nil
}
