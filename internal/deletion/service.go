// Package deletion coordinates source removal across durable storage and
// process-local activity state.
package deletion

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

type observationBatchDeleter interface {
	DeleteObservationRecords(context.Context, []string) error
}

type observationBatchSizer interface {
	MaxObservationDeleteBatchWithinSearchBudget([]capture.SanitizedObservation) (int, error)
}

type knowledgeDeleter interface {
	DeleteAllKnowledge(context.Context) error
}

type chronicleDeleter interface{ DeleteAllChronicles(context.Context) error }

// tombstoneRecorder lets a store keep a small log of user deletions so other
// devices that replicate this memory can replay them. It is optional.
type tombstoneRecorder interface {
	RecordTombstone(context.Context, durable.Tombstone) error
}

var (
	ErrInvalid     = errors.New("deletion: invalid source ID or context")
	ErrUnavailable = errors.New("deletion: encrypted store unavailable")
)

type Service struct {
	store    storage.EncryptedStore
	activity *activity.Engine
}

// NewService requires an encrypted store; the activity engine is optional for
// clients that do not maintain process-local time accounting.
func NewService(store storage.EncryptedStore, engine *activity.Engine) (*Service, error) {
	if store == nil || !store.EncryptedAtRest() {
		return nil, ErrUnavailable
	}
	return &Service{store: store, activity: engine}, nil
}

// DeleteSource first removes the source from live activity memory, then asks
// the store to atomically remove the source and durable derivatives. If the
// store fails, the live memory remains scrubbed and the error is returned so
// the caller can retry persistent cleanup.
func (s *Service) DeleteSource(ctx context.Context, sourceID string) (int, error) {
	if ctx == nil || s == nil || s.store == nil || len(sourceID) != 64 {
		return 0, ErrInvalid
	}
	decoded, err := hex.DecodeString(sourceID)
	if err != nil || hex.EncodeToString(decoded) != sourceID {
		clear(decoded)
		return 0, ErrInvalid
	}
	clear(decoded)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := s.recordTombstone(ctx, sourceID, time.Now()); err != nil {
		return 0, err
	}
	removed := 0
	if s.activity != nil {
		removed, err = s.activity.ForgetSource(sourceID)
		if err != nil {
			return removed, err
		}
	}
	if err := s.store.DeleteObservationRecord(ctx, sourceID); err != nil {
		return removed, err
	}
	return removed, nil
}

// DeleteAll removes every encrypted source through the same cascade as an
// individual deletion. It pages in stable content-ID order so it never clones
// the whole store into memory. On failure, removed is the completed prefix;
// callers may safely retry the operation after fixing the storage error.
func (s *Service) DeleteAll(ctx context.Context) (removed int, err error) {
	if s == nil || s.store == nil || ctx == nil {
		return 0, ErrInvalid
	}
	// The tombstone is written first so a crash mid-delete still replicates.
	if err := s.recordTombstone(ctx, durable.AllSourcesTombstone, time.Now()); err != nil {
		return 0, err
	}
	removed, err = s.deleteMatching(ctx, nil)
	if err != nil {
		return removed, err
	}
	deleter, ok := s.store.(knowledgeDeleter)
	if !ok {
		return removed, ErrUnavailable
	}
	if err := deleter.DeleteAllKnowledge(ctx); err != nil {
		return removed, err
	}
	if clearer, ok := s.store.(importedClearer); ok {
		if err := clearer.ClearImported(ctx); err != nil {
			return removed, err
		}
	}
	if chronicles, ok := s.store.(chronicleDeleter); ok {
		return removed, chronicles.DeleteAllChronicles(ctx)
	}
	return removed, nil
}

// importedClearer lets a store forget import-origin marks on delete-all.
type importedClearer interface{ ClearImported(context.Context) error }

type replicatedKey struct{}

// WithoutTombstone marks a deletion that is itself the replay of another
// device's tombstone, so it must not be logged (and re-exported) again.
func WithoutTombstone(ctx context.Context) context.Context {
	return context.WithValue(ctx, replicatedKey{}, true)
}

func (s *Service) recordTombstone(ctx context.Context, sourceID string, at time.Time) error {
	if replayed, _ := ctx.Value(replicatedKey{}).(bool); replayed {
		return nil
	}
	recorder, ok := s.store.(tombstoneRecorder)
	if !ok {
		return nil
	}
	return recorder.RecordTombstone(ctx, durable.Tombstone{SourceID: sourceID, At: at.UTC()})
}

// DeleteBefore removes only sources whose authenticated capture timestamp is
// strictly earlier than cutoff. Unknown timestamps fail closed; callers must
// not silently interpret missing metadata as expired.
func (s *Service) DeleteBefore(ctx context.Context, cutoff time.Time) (int, error) {
	if cutoff.IsZero() {
		return 0, ErrInvalid
	}
	cutoff = cutoff.UTC()
	return s.deleteMatching(ctx, &cutoff)
}

func (s *Service) deleteMatching(ctx context.Context, cutoff *time.Time) (removed int, err error) {
	if s == nil || s.store == nil || ctx == nil {
		return 0, ErrInvalid
	}
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		page, err := s.store.ListObservationPage(ctx, storage.ObservationPageRequest{
			Limit: storage.MaxObservationPageRows, MaxBytes: storage.MaxObservationPageBytes, After: after,
		})
		if err != nil {
			return removed, err
		}
		if len(page.Observations) == 0 {
			if page.HasMore {
				return removed, ErrInvalid
			}
			return removed, nil
		}
		last := page.Observations[len(page.Observations)-1].ContentID()
		if len(last) != 64 {
			return removed, ErrInvalid
		}
		batchStore, supportsBatch := s.store.(observationBatchDeleter)
		batch := make([]string, 0, 8)
		selected := make([]capture.SanitizedObservation, 0, len(page.Observations))
		for i, observation := range page.Observations {
			if cutoff != nil {
				if len(page.CapturedAt) != len(page.Observations) || page.CapturedAt[i].IsZero() {
					return removed, ErrInvalid
				}
				if !page.CapturedAt[i].Before(*cutoff) {
					continue
				}
			}
			selected = append(selected, observation)
		}
		batchLimit := 8
		if sizer, ok := s.store.(observationBatchSizer); ok {
			if len(selected) > 0 {
				batchLimit, err = sizer.MaxObservationDeleteBatchWithinSearchBudget(selected)
			}
			if err != nil || len(selected) > 0 && batchLimit < 1 {
				if err == nil {
					err = ErrInvalid
				}
				return removed, err
			}
		}
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			if supportsBatch {
				if err := batchStore.DeleteObservationRecords(ctx, batch); err != nil {
					return err
				}
				removed += len(batch)
				batch = batch[:0]
				return nil
			}
			for _, id := range batch {
				if err := s.store.DeleteObservationRecord(ctx, id); err != nil {
					return err
				}
				removed++
			}
			batch = batch[:0]
			return nil
		}
		for _, observation := range selected {
			id := observation.ContentID()
			if len(id) != 64 {
				return removed, ErrInvalid
			}
			if err := ctx.Err(); err != nil {
				return removed, err
			}
			if s.activity != nil {
				if _, err := s.activity.ForgetSource(id); err != nil {
					return removed, err
				}
			}
			batch = append(batch, id)
			if len(batch) == batchLimit || !supportsBatch {
				if err := flush(); err != nil {
					return removed, err
				}
			}
		}
		if err := flush(); err != nil {
			return removed, err
		}
		if last == after {
			return removed, ErrInvalid
		}
		after = last
		if !page.HasMore {
			return removed, nil
		}
	}
}
