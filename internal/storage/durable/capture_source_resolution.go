package durable

import (
	"context"
	"encoding/binary"
	"errors"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"time"
)

// ResolveCaptureSource identifies a whole published capture from its edition or
// genuine indexed part identity. Deleting one excerpt deletes its whole source;
// partial document erasure must never leave an apparently complete document.
func (s *Store) ResolveCaptureSource(ctx context.Context, id string) (string, bool, error) {
	if ctx == nil || !validHash(id) {
		return "", false, &storage.StoreError{Code: storage.Invalid}
	}
	var resolved string
	var found bool
	err := s.view(ctx, func(r bolt.Reader) error {
		var err error
		resolved, found, err = s.resolveCaptureSourceReader(r, id)
		return err
	})
	return resolved, found, err
}
func (s *Store) resolveCaptureSourceReader(r bolt.Reader, id string) (string, bool, error) {
	if _, _, found, err := s.captureEditionReader(r, id); err != nil || found {
		return id, found, err
	}
	mapping, found, err := s.captureIndexPartReader(r, id)
	if err != nil || !found {
		return id, false, err
	}
	edition, summary, present, err := s.captureEditionReader(r, mapping.EditionID)
	if err != nil {
		return "", false, err
	}
	if !present || edition.Publication != mapping.Publication || mapping.Index < 0 || mapping.Index >= summary.PartCount() || capturePartSourceID(mapping.EditionID, mapping.Publication, mapping.Index) != id {
		return "", false, ErrCorrupt
	}
	return mapping.EditionID, true, nil
}

// ListCaptureSourceIDs returns at most 64 real part identities. afterPart=-1
// starts at part zero; indexes are stable within the authenticated publication.
// No part payload is loaded, so native cleanup need not allocate the document.
func (s *Store) ListCaptureSourceIDs(ctx context.Context, id string, afterPart int64, limit int) ([]string, bool, error) {
	if ctx == nil || !validHash(id) || afterPart < -1 || limit < 1 || limit > 64 {
		return nil, false, &storage.StoreError{Code: storage.Invalid}
	}
	var ids []string
	var more bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		resolved, _, err := s.resolveCaptureSourceReader(reader, id)
		if err != nil {
			return err
		}
		edition, summary, found, err := s.captureEditionReader(reader, resolved)
		if err != nil || !found {
			return err
		}
		if afterPart >= summary.PartCount() {
			return &storage.StoreError{Code: storage.Invalid}
		}
		start := afterPart + 1
		end := min(summary.PartCount(), start+int64(limit))
		for i := start; i < end; i++ {
			ids = append(ids, capturePartSourceID(resolved, edition.Publication, i))
		}
		more = end < summary.PartCount()
		return nil
	})
	return ids, more, err
}

// SourceEvidenceSightingAt uses the capture's actual authenticated visit tree.
// Each immutable part shares the whole source's visits, not an invented latest
// timestamp and not only the first publication's capture time.
func (s *Store) SourceEvidenceSightingAt(ctx context.Context, id string, start, end time.Time) (time.Time, bool, error) {
	if ctx == nil || !validHash(id) || !storage.ValidObservationWindow(storage.ObservationWindowRequest{Start: start, End: end, Limit: 1, MaxBytes: storage.MaxObservationPageBytes}) {
		return time.Time{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	var at time.Time
	var legacy bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		mapping, found, err := s.captureIndexPartReader(reader, id)
		if err != nil {
			return err
		}
		if !found {
			legacy = true
			return nil
		}
		edition, summary, live, err := s.captureLiveReader(reader, mapping.EditionID)
		if err != nil || !live {
			return err
		}
		if mapping.Publication != edition.Publication || mapping.Index >= summary.PartCount() {
			return nil
		}
		// The visit time index is ordered, supports exact ranges and validates each
		// result against its immutable receipt/edition membership.
		return s.captureSightingInWindow(ctx, reader, mapping.EditionID, edition, start, end, &at)
	})
	if err != nil {
		return time.Time{}, false, err
	}
	if legacy {
		return s.ObservationSightingAt(ctx, id, start, end)
	}
	return at, !at.IsZero(), nil
}

func (s *Store) captureSightingInWindowBudget(ctx context.Context, reader bolt.Reader, id string, edition captureEditionRecord, start, end time.Time, out *time.Time, budget *search.ExactPage, maxBytes int) (retErr error) {
	defer func() {
		if errors.Is(retErr, ErrTransactionBudget) || errors.Is(retErr, bolt.ErrBudget) {
			retErr = search.ErrIndexBudget
		}
	}()
	latest := time.Unix(0, edition.LastSeen).UTC()
	if !latest.Before(start) && latest.Before(end) {
		*out = latest
		return nil
	}
	if latest.Before(start) {
		return nil
	}
	if edition.IndexVisits != edition.Visits {
		return search.ErrIndexUnavailable
	}
	scoped := s.capturePartIndex()
	digest := scoped.searchLookupDigest(captureIndexVisit, id)
	it := newSearchIndexTreeIterator(ctx, reader, scoped, captureIndexVisit, digest)
	lower := make([]byte, searchIndexTreeOrderBytes)
	binary.BigEndian.PutUint64(lower[1:9], ^(uint64(end.Add(-time.Nanosecond).UnixNano()) ^ (uint64(1) << 63)))
	it.lower = lower
	posting, found, blocked, err := it.next(budget, maxBytes)
	if err != nil {
		return err
	}
	if blocked {
		return search.ErrIndexBudget
	}
	if !found {
		return nil
	}
	visit, present, err := s.captureIndexedVisitReader(captureQueryReader{reader, budget, maxBytes}, id, posting.sourceID)
	if err != nil {
		return err
	}
	if !present || visit.Publication != edition.Publication {
		return ErrCorrupt
	}
	receipt, present, err := s.captureReceiptReader(captureQueryReader{reader, budget, maxBytes}, visit.CaptureID)
	if err != nil {
		return err
	}
	if !present || receipt.EditionID != id || receipt.At != visit.At {
		return ErrCorrupt
	}
	at := time.Unix(0, visit.At).UTC()
	if !at.Before(start) && at.Before(end) {
		*out = at
	}
	return nil
}

func (s *Store) captureSightingInWindow(ctx context.Context, reader bolt.Reader, id string, edition captureEditionRecord, start, end time.Time, out *time.Time) error {
	var budget search.ExactPage
	return s.captureSightingInWindowBudget(ctx, reader, id, edition, start, end, out, &budget, search.MaxIndexBytes)
}
