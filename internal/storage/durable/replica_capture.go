package durable

import (
	"bytes"
	"context"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
)

// ApplyReplicaCapturePart idempotently stages one authenticated origin part.
// It publishes only after the complete summary root and part count validate,
// preserving the origin publication token that participates in every source
// ID. It is the durable write primitive used by the capture sidecar importer.
func (s *Store) ApplyReplicaCapturePart(ctx context.Context, summary capture.CaptureSummary, publication string, part capture.CapturePart) error {
	if ctx == nil || summary.Validate() != nil || !validHash(publication) || part.Validate() != nil || part.CaptureID() != summary.Header().CaptureID() || part.Index() >= summary.PartCount() {
		return &storage.StoreError{Code: storage.Invalid}
	}
	header, summary, part, err := capture.AdmitAuthenticatedReplicaPart(summary, part)
	if err != nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	covered, err := s.ObservationDeletionCovers(ctx, summary.ContentID(), header.CapturedAt())
	if err != nil {
		return err
	}
	if covered {
		return nil
	}
	if edition, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil {
		return err
	} else if found {
		if edition.Publication != publication || edition.Summary.Root() != summary.Root() || edition.Summary.PartCount() != summary.PartCount() {
			return ErrCaptureConflict
		}
		page, err := s.ReadCapturePartsPage(ctx, storage.CapturePartsPageRequest{EditionID: summary.ContentID(), Publication: publication, After: part.Index() - 1, Limit: 1})
		if err != nil {
			return err
		}
		if len(page.Parts) != 1 || page.Parts[0].Index() != part.Index() {
			return ErrCaptureConflict
		}
		stored, err := capture.MarshalAuthenticatedCapturePart(page.Parts[0])
		if err != nil {
			return ErrCorrupt
		}
		defer clear(stored)
		incoming, err := capture.MarshalAuthenticatedCapturePart(part)
		if err != nil {
			return ErrCorrupt
		}
		defer clear(incoming)
		if !bytes.Equal(stored, incoming) {
			return ErrCaptureConflict
		}
		return nil
	}
	if err := s.BeginCapture(ctx, header); err != nil && err != ErrCaptureClosed {
		return err
	}
	if err := s.AppendCaptureParts(ctx, header, []capture.CapturePart{part}); err != nil {
		return err
	}
	if part.Index()+1 == summary.PartCount() {
		_, err := s.publishCaptureWithPublicationHook(ctx, summary, publication, nil)
		return err
	}
	return nil
}
