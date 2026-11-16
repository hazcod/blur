package deletion

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/tasks"
)

type captureDeleter interface {
	DeleteCapturesBefore(context.Context, *time.Time, func(string, []tasks.Item) error) (int, error)
}

type captureFamilyDeleter interface {
	ForgetCaptureFamily(context.Context, string, time.Time, func(string, []tasks.Item) error) (int, error)
}

type preparedSourceDeleter interface {
	DeleteSourceRecordWithPrepare(context.Context, string, func(string, []tasks.Item) error) error
	DeleteSourceRecordBeforeWithPrepare(context.Context, string, time.Time, bool, func(string, []tasks.Item) error) (bool, error)
}
type captureSourceResolver interface {
	ResolveCaptureSource(context.Context, string) (string, bool, error)
}

func (s *Service) deletePreparedCapture(ctx context.Context, sourceID string) (int, bool, error) {
	resolver, ok := s.store.(captureSourceResolver)
	if !ok {
		return 0, false, nil
	}
	parent, found, err := resolver.ResolveCaptureSource(ctx, sourceID)
	if err != nil || !found {
		return 0, false, err
	}
	store, ok := s.store.(preparedSourceDeleter)
	if !ok {
		return 0, true, ErrUnavailable
	}
	removed, err := s.withCaptureDeletionGuard(ctx, func(guardCtx context.Context, prepare func(string, []tasks.Item) error) (int, error) {
		at := time.Now()
		if err := s.recordTombstone(guardCtx, sourceID, at); err != nil {
			return 0, err
		}
		if parent != sourceID {
			if err := s.recordTombstone(guardCtx, parent, at); err != nil {
				return 0, err
			}
		}
		if err := store.DeleteSourceRecordWithPrepare(guardCtx, sourceID, prepare); err != nil {
			return 0, err
		}
		return 1, nil
	})
	return removed, true, err
}

func (s *Service) deleteCaptureMatching(ctx context.Context, cutoff *time.Time) (int, error) {
	store, ok := s.store.(captureDeleter)
	if !ok {
		return 0, nil
	}
	return s.withCaptureDeletionGuard(ctx, func(guardCtx context.Context, prepare func(string, []tasks.Item) error) (int, error) {
		return store.DeleteCapturesBefore(guardCtx, cutoff, prepare)
	})
}

// ForgetCaptureFamily includes native reminder preparation and process-local
// activity cleanup before the store commits a restart-resumable family intent.
func (s *Service) ForgetCaptureFamily(ctx context.Context, familyID string, cutoff time.Time) (int, error) {
	if ctx == nil || s == nil || s.store == nil {
		return 0, ErrInvalid
	}
	store, ok := s.store.(captureFamilyDeleter)
	if !ok {
		return 0, ErrUnavailable
	}
	if err := s.recoverCommittedDeletion(ctx); err != nil {
		return 0, err
	}
	return s.withCaptureDeletionGuard(ctx, func(guardCtx context.Context, prepare func(string, []tasks.Item) error) (int, error) {
		return store.ForgetCaptureFamily(guardCtx, familyID, cutoff, prepare)
	})
}

func (s *Service) withCaptureDeletionGuard(ctx context.Context, run func(context.Context, func(string, []tasks.Item) error) (int, error)) (removed int, err error) {
	action := func(guardCtx context.Context, cancel func([]tasks.Item) error) error {
		var runErr error
		removed, runErr = run(guardCtx, func(id string, items []tasks.Item) error {
			if cancel != nil {
				if err := cancel(items); err != nil {
					return err
				}
			}
			if s.activity != nil {
				_, err := s.activity.ForgetSource(id)
				return err
			}
			return nil
		})
		return runErr
	}
	if held, _ := ctx.Value(allNotificationGuardKey{}).(bool); held {
		err = action(ctx, nil)
	} else if cleanup := s.notificationCleanup(); cleanup != nil {
		guard, ok := cleanup.(conditionalNotificationCleanup)
		if !ok {
			return 0, ErrUnavailable
		}
		err = guard.WithSourceDeletionGuard(ctx, action)
	} else {
		err = action(ctx, nil)
	}
	return removed, err
}
