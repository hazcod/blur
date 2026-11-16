package semantic

import (
	"context"
	"errors"
	"fmt"

	"github.com/hazcod/blur/internal/storage"
)

// Multipart publications use an independent directory cursor. Their source IDs
// remain publication-scoped part IDs; no legacy observation alias is created.
func (s *Service) backfillCaptures(ctx context.Context, index *Index, epoch uint64, identity Model, checkpoint *Checkpoint) error {
	reader, captures := s.sources.(storage.CaptureEvidencePageReader)
	if captures && !checkpoint.CaptureComplete {
		for processed := 0; processed < 64; {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !s.current(epoch) || !s.canRead() {
				return context.Canceled
			}
			before := checkpoint.CaptureAfter
			page, err := reader.ListCaptureEvidencePage(ctx, storage.CaptureEvidencePageRequest{After: before, Limit: 4, MaxBytes: storage.MaxObservationPageBytes})
			if errors.Is(err, storage.ErrCaptureCursorChanged) {
				checkpoint.CaptureAfter, checkpoint.CaptureComplete = "", false
				if err := s.saveCaptureCheckpoint(ctx, epoch, index, checkpoint); err != nil {
					return err
				}
				return errMoreWork
			}
			if err != nil {
				return err
			}
			if len(page.Sources) != len(page.CapturedAt) {
				return ErrInvalid
			}
			for number, source := range page.Sources {
				provenance, multipart := source.Provenance()
				if source.Validate() != nil || !multipart {
					return ErrInvalid
				}
				if !index.HasDocument(source.SourceID()) {
					if err := s.indexSource(ctx, index, epoch, identity, source, page.CapturedAt[number]); err != nil {
						return err
					}
				}
				checkpoint.CaptureAfter = fmt.Sprintf("%s:%s:%d", provenance.EditionID, provenance.Publication, provenance.PartIndex)
				if err := s.saveCaptureCheckpoint(ctx, epoch, index, checkpoint); err != nil {
					return err
				}
				s.publishCounts(index, epoch, "indexing", false)
				processed++
				if processed >= 64 {
					return errMoreWork
				}
			}
			if !storage.ValidCaptureEvidenceCursor(page.Next) {
				return ErrInvalid
			}
			if page.Next != checkpoint.CaptureAfter {
				checkpoint.CaptureAfter = page.Next
				if err := s.saveCaptureCheckpoint(ctx, epoch, index, checkpoint); err != nil {
					return err
				}
			}
			if page.Pending {
				return errMoreWork
			}
			if !page.HasMore {
				checkpoint.CaptureComplete = true
				if err := s.saveCaptureCheckpoint(ctx, epoch, index, checkpoint); err != nil {
					return err
				}
				break
			}
			if len(page.Sources) == 0 {
				if checkpoint.CaptureAfter == before {
					return ErrInvalid
				}
				processed++ // Bound pages that only skipped fenced editions.
			}
		}
		if !checkpoint.CaptureComplete {
			return errMoreWork
		}
	}
	s.mu.Lock()
	repair := s.repairAfterPass
	s.repairAfterPass = false
	s.mu.Unlock()
	if repair {
		checkpoint.After, checkpoint.CaptureAfter = "", ""
		checkpoint.Complete, checkpoint.CaptureComplete = false, false
		if err := s.saveCaptureCheckpoint(ctx, epoch, index, checkpoint); err != nil {
			return err
		}
		return errMoreWork
	}
	s.publishCounts(index, epoch, "ready", true)
	return nil
}

func (s *Service) saveCaptureCheckpoint(ctx context.Context, epoch uint64, index *Index, checkpoint *Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.current(epoch) || !s.canRead() {
		return context.Canceled
	}
	indexed, skipped, _ := index.Counts()
	checkpoint.Indexed, checkpoint.Skipped = uint64(indexed), uint64(skipped)
	return s.repository.PutSemanticCheckpoint(ctx, *checkpoint)
}
