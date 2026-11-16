package deletion

import (
	"context"

	"github.com/hazcod/blur/internal/tasks"
)

type noteDocumentDeleter interface {
	DeleteNoteDocumentWithPrepare(context.Context, string, uint64, func(string, []tasks.Item) error) error
}

// DeleteNoteDocument cancels every revision's native reminders before the
// authenticated, restart-resumable erasure intent is committed.
func (s *Service) DeleteNoteDocument(ctx context.Context, id string, expected uint64) error {
	if ctx == nil || s == nil || s.store == nil {
		return ErrInvalid
	}
	store, ok := s.store.(noteDocumentDeleter)
	if !ok {
		return ErrUnavailable
	}
	if err := s.recoverCommittedDeletion(ctx); err != nil {
		return err
	}
	_, err := s.withCaptureDeletionGuard(ctx, func(guardCtx context.Context, prepare func(string, []tasks.Item) error) (int, error) {
		return 0, store.DeleteNoteDocumentWithPrepare(guardCtx, id, expected, prepare)
	})
	return err
}
