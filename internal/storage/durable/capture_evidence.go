package durable

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func (s *Store) GetSourceEvidence(ctx context.Context, id string) (capture.Evidence, bool, error) {
	e, _, found, err := s.GetSourceEvidenceWithCaptureTime(ctx, id)
	return e, found, err
}
func (s *Store) GetSourceEvidenceWithCaptureTime(ctx context.Context, id string) (capture.Evidence, time.Time, bool, error) {
	if ctx == nil || !validHash(id) {
		return capture.Evidence{}, time.Time{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	var evidence capture.Evidence
	var at time.Time
	var found bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		evidence, at, found, err = s.sourceEvidenceReader(reader, id)
		return err
	})
	if err != nil {
		return capture.Evidence{}, time.Time{}, false, err
	}
	return evidence, at, found, nil
}

// sourceEvidenceReader operates inside the caller's existing authenticated
// snapshot or write transaction. It never opens a nested store transaction.
func (s *Store) sourceEvidenceReader(reader bolt.Reader, id string) (capture.Evidence, time.Time, bool, error) {
	observation, found, err := s.getObservationReader(reader, id)
	if err != nil {
		return capture.Evidence{}, time.Time{}, false, err
	}
	if found {
		evidence, err := capture.ObservationEvidence(observation)
		if err != nil {
			return capture.Evidence{}, time.Time{}, false, ErrCorrupt
		}
		at, err := s.observationCapturedAtReader(reader, id)
		return evidence, at, err == nil, err
	}
	mapping, found, err := s.captureIndexPartReader(reader, id)
	if err != nil || !found {
		return capture.Evidence{}, time.Time{}, false, err
	}
	edition, summary, live, err := s.captureLiveReader(reader, mapping.EditionID)
	if err != nil || !live {
		return capture.Evidence{}, time.Time{}, false, err
	}
	if edition.Publication != mapping.Publication {
		return capture.Evidence{}, time.Time{}, false, nil
	}
	if mapping.Range {
		evidence, err := s.captureRangeEvidenceReader(reader, mapping, edition, summary)
		return evidence, summary.Header().CapturedAt(), err == nil, err
	}
	part, err := s.captureMemberPartReader(reader, edition, summary, mapping.Index)
	if err != nil {
		return capture.Evidence{}, time.Time{}, false, err
	}
	if len(part.Text()) != mapping.Bytes {
		return capture.Evidence{}, time.Time{}, false, ErrCorrupt
	}
	evidence, err := capture.CapturePartEvidence(summary, part, edition.Publication)
	if err != nil || evidence.SourceID() != id {
		return capture.Evidence{}, time.Time{}, false, ErrCorrupt
	}
	return evidence, summary.Header().CapturedAt(), true, nil
}

// ListCaptureEvidencePage walks only published editions and indexed canonical
// parts, in bounded rounds. Pending distinguishes an unfinished index from an
// empty directory; callers must not mark backfill complete in that state.
func (s *Store) ListCaptureEvidencePage(ctx context.Context, request storage.CaptureEvidencePageRequest) (storage.CaptureEvidencePage, error) {
	page := storage.CaptureEvidencePage{Next: request.After}
	if ctx == nil || !storage.ValidCaptureEvidencePage(request) {
		return storage.CaptureEvidencePage{}, &storage.StoreError{Code: storage.Invalid}
	}
	err := s.view(ctx, func(reader bolt.Reader) error {
		catalog, err := s.captureCatalogReader(reader, "published")
		if err != nil {
			return err
		}
		id := catalog.Head
		index := int64(0)
		if request.After != "" {
			cursor := strings.Split(request.After, ":")
			id = cursor[0]
			r, summary, found, err := s.captureEditionReader(reader, id)
			if err != nil {
				return err
			}
			last, err := strconv.ParseInt(cursor[2], 10, 64)
			if err != nil {
				return &storage.StoreError{Code: storage.Invalid}
			}
			if !found || r.Publication != cursor[1] || last >= summary.PartCount() {
				return ErrCaptureCursorChanged
			}
			index = last + 1
		}
		for scanned := 0; id != "" && scanned < 32; scanned++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			r, summary, live, err := s.captureLiveReader(reader, id)
			if err != nil {
				return err
			}
			if r.Version == 0 || index > summary.PartCount() {
				return ErrCaptureCursorChanged
			}
			if !live {
				page.Next = fmt.Sprintf("%s:%s:%d", id, r.Publication, summary.PartCount()-1)
				id, index = r.Next, 0
				continue
			}
			for index < summary.PartCount() {
				if index >= r.IndexPart {
					page.Pending, page.HasMore = true, true
					return nil
				}
				sourceID := capturePartSourceID(id, r.Publication, index)
				evidence, at, found, err := s.sourceEvidenceReader(reader, sourceID)
				if err != nil {
					return err
				}
				if !found {
					return ErrCorrupt
				}
				size := len(evidence.Text()) + len(evidence.AppID()) + len(evidence.Domain())
				if len(page.Sources) >= request.Limit || size > request.MaxBytes-page.Bytes {
					page.HasMore = true
					return nil
				}
				page.Sources = append(page.Sources, evidence)
				page.CapturedAt = append(page.CapturedAt, at)
				page.Bytes += size
				page.Next = fmt.Sprintf("%s:%s:%d", id, r.Publication, index)
				index++
			}
			next := r.Next
			if next != "" {
				neighbor, _, found, err := s.captureEditionReader(reader, next)
				if err != nil {
					return err
				}
				if !found || neighbor.Prev != id {
					return ErrCorrupt
				}
			} else if id != catalog.Tail {
				return ErrCorrupt
			}
			id, index = next, 0
		}
		page.HasMore = id != ""
		return nil
	})
	if err != nil {
		return storage.CaptureEvidencePage{}, err
	}
	return page, nil
}
