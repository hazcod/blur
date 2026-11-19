// Package deletion coordinates source removal across durable storage and
// process-local activity state.
package deletion

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

type observationBatchDeleter interface {
	DeleteObservationRecords(context.Context, []string) error
}

type observationBatchSizer interface {
	MaxObservationDeleteBatchWithinSearchBudget([]capture.SanitizedObservation) (int, error)
}

type sourceDeletionRecovery interface{ RecoverSourceDeletion(context.Context) error }

func (s *Service) recoverCommittedDeletion(ctx context.Context) error {
	if recovery, ok := s.store.(sourceDeletionRecovery); ok {
		return recovery.RecoverSourceDeletion(ctx)
	}
	return nil
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
	store         storage.EncryptedStore
	activity      *activity.Engine
	cleanupMu     sync.RWMutex
	notifications NotificationCleanup
}

type NotificationCleanup interface {
	GuardSources(context.Context, []string) (func(), error)
	GuardAll(context.Context) (func(), error)
}

func (s *Service) SetNotificationCleanup(cleanup NotificationCleanup) {
	s.cleanupMu.Lock()
	s.notifications = cleanup
	s.cleanupMu.Unlock()
}
func (s *Service) notificationCleanup() NotificationCleanup {
	s.cleanupMu.RLock()
	defer s.cleanupMu.RUnlock()
	return s.notifications
}

type allNotificationGuardKey struct{}

func (s *Service) guardSources(ctx context.Context, ids []string) (func(), error) {
	if held, _ := ctx.Value(allNotificationGuardKey{}).(bool); held {
		return func() {}, nil
	}
	if cleanup := s.notificationCleanup(); cleanup != nil {
		return cleanup.GuardSources(ctx, ids)
	}
	return func() {}, nil
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
	if err := s.recoverCommittedDeletion(ctx); err != nil {
		return 0, err
	}
	if removed, handled, err := s.deletePreparedCapture(ctx, sourceID); handled || err != nil {
		return removed, err
	}
	release, err := s.guardSources(ctx, []string{sourceID})
	if err != nil {
		return 0, err
	}
	defer release()
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

type conditionalSourceDeleter interface {
	DeleteObservationRecordBefore(context.Context, string, time.Time, bool, func([]tasks.Item) error) (bool, error)
}
type conditionalNotificationCleanup interface {
	WithSourceDeletionGuard(context.Context, func(context.Context, func([]tasks.Item) error) error) error
}

// DeleteSourceThrough applies an original replicated cutoff, preserving any
// primary captured later. It never invents a newer deletion timestamp.
func (s *Service) DeleteSourceThrough(ctx context.Context, sourceID string, cutoff time.Time) (int, error) {
	return s.deleteSourceAtCutoff(ctx, sourceID, cutoff, true)
}

func (s *Service) deleteSourceAtCutoff(ctx context.Context, sourceID string, cutoff time.Time, inclusive bool) (removed int, err error) {
	if s == nil || s.store == nil || ctx == nil || !durable.ValidCapturedAt(cutoff) {
		return 0, ErrInvalid
	}
	store, ok := s.store.(conditionalSourceDeleter)
	if !ok {
		return 0, ErrUnavailable
	}
	if err := s.recoverCommittedDeletion(ctx); err != nil {
		return 0, err
	}
	if prepared, ok := s.store.(preparedSourceDeleter); ok {
		return s.withCaptureDeletionGuard(ctx, func(guardCtx context.Context, prepare func(string, []tasks.Item) error) (int, error) {
			erased, err := prepared.DeleteSourceRecordBeforeWithPrepare(guardCtx, sourceID, cutoff, inclusive, prepare)
			if erased {
				return 1, err
			}
			return 0, err
		})
	}
	action := func(guardCtx context.Context, cancel func([]tasks.Item) error) error {
		erased, err := store.DeleteObservationRecordBefore(guardCtx, sourceID, cutoff, inclusive, func(items []tasks.Item) error {
			if cancel != nil {
				if err := cancel(items); err != nil {
					return err
				}
			}
			if s.activity != nil {
				var err error
				_, err = s.activity.ForgetSource(sourceID)
				return err
			}
			return nil
		})
		if erased {
			removed = 1
		}
		return err
	}
	if cleanup := s.notificationCleanup(); cleanup != nil {
		guard, ok := cleanup.(conditionalNotificationCleanup)
		if !ok {
			return 0, ErrUnavailable
		}
		err = guard.WithSourceDeletionGuard(ctx, action)
		return removed, err
	}
	err = action(ctx, nil)
	return removed, err
}

// DeleteAll removes every encrypted source through the same cascade as an
// individual deletion. It pages in stable content-ID order so it never clones
// the whole store into memory. On failure, removed is the completed prefix;
// callers may safely retry the operation after fixing the storage error.
func (s *Service) DeleteAll(ctx context.Context) (removed int, err error) {
	if s == nil || s.store == nil || ctx == nil {
		return 0, ErrInvalid
	}
	if err := s.recoverCommittedDeletion(ctx); err != nil {
		return 0, err
	}
	if cleanup := s.notificationCleanup(); cleanup != nil {
		release, err := cleanup.GuardAll(ctx)
		if err != nil {
			return 0, err
		}
		defer release()
		ctx = context.WithValue(ctx, allNotificationGuardKey{}, true)
	}
	// The tombstone is written first so a crash mid-delete still replicates.
	if err := s.recordTombstone(ctx, durable.AllSourcesTombstone, time.Now()); err != nil {
		return 0, err
	}
	removed, err = s.deleteMatching(ctx, nil)
	if err != nil {
		return removed, err
	}
	captures, err := s.deleteCaptureMatching(ctx, nil)
	removed += captures
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
	if taskStore, ok := s.store.(interface{ DeleteAllTasks(context.Context) error }); ok {
		if err := taskStore.DeleteAllTasks(ctx); err != nil {
			return removed, err
		}
	}
	if profileStore, ok := s.store.(interface{ DeleteAllProfile(context.Context) error }); ok {
		if err := profileStore.DeleteAllProfile(ctx); err != nil {
			return removed, err
		}
	}
	if meetingStore, ok := s.store.(interface {
		DeleteAllMeetings(context.Context) (int, error)
	}); ok {
		if _, err := meetingStore.DeleteAllMeetings(ctx); err != nil {
			return removed, err
		}
	}
	if clearer, ok := s.store.(importedClearer); ok {
		if err := clearer.ClearImported(ctx); err != nil {
			return removed, err
		}
	}
	// Ask question history cites sources; delete-all clears the whole namespace.
	if questions, ok := s.store.(storage.QuestionHistory); ok {
		if err := questions.DeleteAllQuestions(ctx); err != nil {
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
	removed, err := s.deleteMatching(ctx, &cutoff)
	if err != nil {
		return removed, err
	}
	captures, err := s.deleteCaptureMatching(ctx, &cutoff)
	removed += captures
	if err != nil {
		return removed, err
	}
	if meetingStore, ok := s.store.(interface {
		DeleteMeetingsBefore(context.Context, time.Time) (int, error)
	}); ok {
		_, err = meetingStore.DeleteMeetingsBefore(ctx, cutoff)
	}
	return removed, err
}

func (s *Service) deleteMatching(ctx context.Context, cutoff *time.Time) (removed int, err error) {
	if s == nil || s.store == nil || ctx == nil {
		return 0, ErrInvalid
	}
	if err := s.recoverCommittedDeletion(ctx); err != nil {
		return 0, err
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
				if _, conditional := s.store.(conditionalSourceDeleter); conditional {
					n, err := s.deleteSourceAtCutoff(ctx, observation.ContentID(), *cutoff, false)
					removed += n
					if err != nil {
						return removed, err
					}
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
			release, err := s.guardSources(ctx, batch)
			if err != nil {
				return err
			}
			defer release()
			if supportsBatch {
				completed, err := deleteBoundedBatch(ctx, batchStore, batch)
				removed += completed
				if err != nil {
					return err
				}
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

// A valid batch can exceed the physical byte cap as source-linked vectors or
// other projections grow. Invalid transactions roll back atomically, so split
// only that classified rejection. Never retry corruption or generic I/O errors.
func deleteBoundedBatch(ctx context.Context, store observationBatchDeleter, ids []string) (int, error) {
	err := store.DeleteObservationRecords(ctx, ids)
	if err == nil {
		return len(ids), nil
	}
	var rejected *storage.StoreError
	if len(ids) < 2 || !errors.As(err, &rejected) || rejected.Code != storage.Invalid {
		return 0, err
	}
	middle := len(ids) / 2
	first, err := deleteBoundedBatch(ctx, store, ids[:middle])
	if err != nil {
		return first, err
	}
	second, err := deleteBoundedBatch(ctx, store, ids[middle:])
	return first + second, err
}
