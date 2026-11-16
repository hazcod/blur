package storage

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/capture"
)

type ObservationReader interface {
	GetObservation(context.Context, string) (capture.SanitizedObservation, bool, error)
}
type ObservationTimeReader interface {
	GetObservationWithCaptureTime(context.Context, string) (capture.SanitizedObservation, time.Time, bool, error)
}
type SourceEvidenceReader interface {
	GetSourceEvidence(context.Context, string) (capture.Evidence, bool, error)
}
type SourceEvidenceTimeReader interface {
	GetSourceEvidenceWithCaptureTime(context.Context, string) (capture.Evidence, time.Time, bool, error)
}

// ReadSourceEvidence preserves legacy backend compatibility without disguising
// multipart identities as legacy observation ContentIDs.
func ReadSourceEvidence(ctx context.Context, reader ObservationReader, id string) (capture.Evidence, bool, error) {
	if ctx == nil || reader == nil || !validHash(id) {
		return capture.Evidence{}, false, &StoreError{Code: Invalid}
	}
	if err := ctx.Err(); err != nil {
		return capture.Evidence{}, false, err
	}
	if sources, ok := reader.(SourceEvidenceReader); ok {
		evidence, found, err := sources.GetSourceEvidence(ctx, id)
		if err != nil || !found {
			return capture.Evidence{}, false, err
		}
		if evidence.Validate() != nil || evidence.SourceID() != id {
			return capture.Evidence{}, false, &StoreError{Code: Invalid}
		}
		return evidence, true, nil
	}
	observation, found, err := reader.GetObservation(ctx, id)
	if err != nil || !found {
		return capture.Evidence{}, false, err
	}
	evidence, err := capture.ObservationEvidence(observation)
	if err == nil && evidence.SourceID() != id {
		return capture.Evidence{}, false, &StoreError{Code: Invalid}
	}
	return evidence, err == nil, err
}

func ReadSourceEvidenceWithCaptureTime(ctx context.Context, reader ObservationTimeReader, id string) (capture.Evidence, time.Time, bool, error) {
	if ctx == nil || reader == nil || !validHash(id) {
		return capture.Evidence{}, time.Time{}, false, &StoreError{Code: Invalid}
	}
	if err := ctx.Err(); err != nil {
		return capture.Evidence{}, time.Time{}, false, err
	}
	if sources, ok := reader.(SourceEvidenceTimeReader); ok {
		evidence, at, found, err := sources.GetSourceEvidenceWithCaptureTime(ctx, id)
		if err != nil || !found {
			return capture.Evidence{}, time.Time{}, false, err
		}
		if evidence.Validate() != nil || evidence.SourceID() != id {
			return capture.Evidence{}, time.Time{}, false, &StoreError{Code: Invalid}
		}
		return evidence, at, true, nil
	}
	observation, at, found, err := reader.GetObservationWithCaptureTime(ctx, id)
	if err != nil || !found {
		return capture.Evidence{}, time.Time{}, false, err
	}
	evidence, err := capture.ObservationEvidence(observation)
	if err == nil && evidence.SourceID() != id {
		return capture.Evidence{}, time.Time{}, false, &StoreError{Code: Invalid}
	}
	return evidence, at, err == nil, err
}
