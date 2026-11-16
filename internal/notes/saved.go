package notes

import (
	"context"
	"errors"
	"time"

	"github.com/hazcod/blur/internal/capture"
)

var ErrSourceUnavailable = errors.New("notes: saved source unavailable")

// SavedNoteReader reads the exact immutable sanitized evidence and its original
// durable timestamp. A processor never recreates evidence with SaveNote.
type SavedNoteReader interface {
	GetObservationWithCaptureTime(context.Context, string) (capture.SanitizedObservation, time.Time, bool, error)
}

// CommitGuard makes a brief publication atomic with the caller's current
// consent and memory epoch. It must invoke write at most once, synchronously.
// Guard locks are acquired before this service's model-generation read lock.
type CommitGuard func(write func() error) error

// ProcessSavedNote extracts optional suggestions from an already saved note.
// It never saves/resends another source or activates a task. Repository proposal
// writes must revalidate source membership in their write transaction, as the
// durable store does. Desktop callers additionally supply their deletion and
// provider-consent publication fence.
func (s *Service) ProcessSavedNote(ctx context.Context, sourceID string, guards ...CommitGuard) (Result, error) {
	if s == nil || s.repository == nil || ctx == nil || len(sourceID) != 64 || len(guards) > 1 || len(guards) == 1 && guards[0] == nil {
		return Result{}, ErrInvalid
	}
	for _, c := range sourceID {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return Result{}, ErrInvalid
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	reader, ok := s.repository.(SavedNoteReader)
	if !ok {
		return Result{}, ErrSourceUnavailable
	}
	source, at, found, err := reader.GetObservationWithCaptureTime(ctx, sourceID)
	if err != nil {
		return Result{}, err
	}
	if !found || source.Validate() != nil || source.ContentID() != sourceID || source.AppID() != "sh.blur.notes" || !validNoteDomain(source.Domain()) || at.IsZero() {
		return Result{}, ErrSourceUnavailable
	}
	if current, ok := s.repository.(interface {
		IsCurrentNoteSource(context.Context, string) (bool, error)
	}); ok {
		live, err := current.IsCurrentNoteSource(ctx, sourceID)
		if err != nil {
			return Result{}, err
		}
		if !live {
			return Result{}, ErrSourceUnavailable
		}
	}
	result, generation, deferred := s.prepare(ctx, source, at, true)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if deferred != nil {
		return result, deferred
	}
	if result.Processing != ProcessingModel {
		if result.Processing == ProcessingDeterministic {
			result.Processing = ProcessingUnavailable
		}
		return result, nil
	}
	write := func() error {
		s.mu.RLock()
		defer s.mu.RUnlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.modelCtx != generation || generation.Err() != nil {
			return context.Canceled
		}
		current, savedAt, found, err := reader.GetObservationWithCaptureTime(ctx, sourceID)
		if err != nil {
			return err
		}
		if !found || current.ContentID() != sourceID || !savedAt.Equal(at) {
			return ErrSourceUnavailable
		}
		if len(result.Proposals) == 0 {
			return nil
		}
		return s.repository.SaveProposals(ctx, sourceID, result.Proposals)
	}
	if len(guards) == 1 {
		err = guards[0](write)
	} else {
		err = write()
	}
	if err != nil {
		result.Processing = ProcessingSuggestionsUnavailable
	}
	return result, err
}
