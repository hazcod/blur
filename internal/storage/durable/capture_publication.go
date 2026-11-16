package durable

import (
	"context"
	"github.com/hazcod/blur/internal/capture"
)

type CapturePublicationReceipt struct {
	Edition        CaptureEdition
	Committed, New bool
	SourceID       string
}

// PublishCaptureWithSourceReceipt derives the first source identity inside the
// authenticated publication transaction, including duplicate publications.
// It commits only: callers release preference admission before draining a
// duplicate private payload with a bounded DiscardCaptureStage context.
// Unfinished private cleanup is also resumed by durable startup recovery.
func (s *Store) PublishCaptureWithSourceReceipt(ctx context.Context, summary capture.CaptureSummary) (CapturePublicationReceipt, error) {
	edition, err := s.publishCaptureWithHook(ctx, summary, nil)
	if err != nil {
		return CapturePublicationReceipt{}, err
	}
	receipt := CapturePublicationReceipt{Edition: edition, Committed: true, New: edition.created, SourceID: edition.firstSourceID}

	return receipt, nil
}
