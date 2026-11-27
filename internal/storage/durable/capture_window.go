package durable

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

type sourceWindowItem struct {
	source capture.Evidence
	at     time.Time
	group  string
	part   int64
}

// ListSourceEvidenceWindow uses the legacy encrypted visit index and the
// encrypted edition/hour directory. It never enumerates the capture catalog or
// reads whole documents. Parts remain independently authenticated bounded reads.
func (s *Store) ListSourceEvidenceWindow(ctx context.Context, request storage.ObservationWindowRequest) (storage.SourceEvidenceWindow, error) {
	if ctx == nil || !storage.ValidObservationWindow(request) {
		return storage.SourceEvidenceWindow{}, &storage.StoreError{Code: storage.Invalid}
	}
	if s == nil || s.state == nil {
		return storage.SourceEvidenceWindow{}, &storage.StoreError{Code: storage.Closed}
	}
	// Keep legacy and multipart sources in one stable publication epoch while
	// their independently bounded read quanta are accumulated and ranked.
	s.state.publicationMu.RLock()
	defer s.state.publicationMu.RUnlock()
	group, cursorPart := request.BeforeSourceID, int64(-1)
	if group != "" {
		err := s.view(ctx, func(r bolt.Reader) error {
			mapping, found, err := s.captureIndexPartReader(r, request.BeforeSourceID)
			if err != nil {
				return err
			}
			if found {
				edition, summary, live, err := s.captureLiveReader(r, mapping.EditionID)
				if err != nil {
					return err
				}
				if !live || edition.Publication != mapping.Publication || mapping.Index >= summary.PartCount() {
					return ErrCaptureCursorChanged
				}
				group, cursorPart = mapping.EditionID, mapping.Index
			} else {
				_, live, err := s.getObservationReader(r, request.BeforeSourceID)
				if err != nil {
					return err
				}
				if !live {
					return ErrCaptureCursorChanged
				}
			}
			return nil
		})
		if err != nil {
			return storage.SourceEvidenceWindow{}, err
		}
	}
	legacyRequest := request
	legacyRequest.BeforeSourceID = group
	legacy, err := s.listObservationWindow(ctx, legacyRequest)
	if err != nil {
		return storage.SourceEvidenceWindow{}, err
	}
	items := make([]sourceWindowItem, 0, 2*request.Limit)
	for i, o := range legacy.Observations {
		e, err := capture.ObservationEvidence(o)
		if err != nil {
			return storage.SourceEvidenceWindow{}, ErrCorrupt
		}
		items = append(items, sourceWindowItem{e, legacy.CapturedAt[i], e.SourceID(), -1})
	}
	documents, pending, more, err := s.captureWindowItems(ctx, request, group, cursorPart)
	if err != nil {
		return storage.SourceEvidenceWindow{}, err
	}
	items = append(items, documents...)
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if !a.at.Equal(b.at) {
			return a.at.After(b.at)
		}
		if a.group != b.group {
			return a.group > b.group
		}
		return a.part < b.part
	})
	page := storage.SourceEvidenceWindow{Pending: pending, HasMore: legacy.HasMore || more}
	for _, item := range items {
		size := len(item.source.Text()) + len(item.source.AppID()) + len(item.source.Domain())
		if len(page.Sources) >= request.Limit || size > request.MaxBytes-page.Bytes {
			page.HasMore = true
			break
		}
		page.Sources = append(page.Sources, item.source)
		page.CapturedAt = append(page.CapturedAt, item.at)
		page.NextCapturedAt, page.NextSourceID = item.at, item.source.SourceID()
		page.Bytes += size
	}
	return page, nil
}

func (s *Store) captureWindowItems(ctx context.Context, request storage.ObservationWindowRequest, cursorGroup string, cursorPart int64) ([]sourceWindowItem, bool, bool, error) {
	var items []sourceWindowItem
	var pending, more bool
	start, end := request.Start.UTC(), request.End.UTC()
	if err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		pending, err = s.captureIndexPendingReader(reader)
		return err
	}); err != nil {
		return nil, false, false, err
	}
	cursor := newObservationTimelineScanCursor(start, end)
	seen := map[string]bool{}
	candidates := make([]captureWindowCandidate, 0, request.Limit+1)
	keep := request.Limit + 1
	for !cursor.done {
		if err := ctx.Err(); err != nil {
			return nil, false, false, err
		}
		rows, next, err := s.observationTimelineQuantum(ctx, captureTimelineNS, cursor)
		if err != nil {
			return nil, false, false, err
		}
		cursor = next
		if len(rows) == 0 {
			continue
		}
		err = s.viewWithReadLimit(ctx, bolt.MaxReadTransactionOps, func(reader bolt.Reader) error {
			for _, row := range rows {
				if seen[row.id] {
					continue
				}
				edition, summary, live, err := s.captureLiveReader(reader, row.id)
				if err != nil {
					return err
				}
				if !live {
					continue
				}
				header := summary.Header()
				if request.AppID != "" && header.AppID() != request.AppID || request.Domain != "" && header.Domain() != request.Domain {
					continue
				}
				excluded := false
				for _, app := range request.ExcludeAppIDs {
					excluded = excluded || app == header.AppID()
				}
				if excluded {
					continue
				}
				var actual time.Time
				var budget search.ExactPage
				hour := row.at.Truncate(time.Hour)
				err = s.captureSightingInWindowBudget(ctx, reader, row.id, edition, maxTime(start, hour), minTime(end, hour.Add(time.Hour)), &actual, &budget, search.MaxIndexBytes)
				if errors.Is(err, search.ErrIndexUnavailable) {
					pending = true
					continue
				}
				if err != nil {
					return err
				}
				if actual.IsZero() {
					continue
				}
				if actual.After(row.at) {
					return ErrCorrupt
				}
				seen[row.id] = true
				if !request.BeforeCapturedAt.IsZero() && (actual.After(request.BeforeCapturedAt) || actual.Equal(request.BeforeCapturedAt) && row.id > cursorGroup) {
					continue
				}
				if actual.Equal(request.BeforeCapturedAt) && row.id == cursorGroup && cursorPart < 0 {
					continue
				}
				if edition.IndexPart == 0 {
					pending = true
					more = true
					continue
				}
				if edition.IndexPart < summary.PartCount() {
					pending = true
				}
				row.at = actual
				candidates = append(candidates, row)
				if len(candidates) > keep {
					sort.Slice(candidates, func(i, j int) bool {
						if !candidates[i].at.Equal(candidates[j].at) {
							return candidates[i].at.After(candidates[j].at)
						}
						return candidates[i].id > candidates[j].id
					})
					candidates = candidates[:keep]
				}
			}
			return nil
		})
		if err != nil {
			return nil, false, false, err
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].at.Equal(candidates[j].at) {
			return candidates[i].at.After(candidates[j].at)
		}
		return candidates[i].id > candidates[j].id
	})
	err := s.viewWithReadLimit(ctx, bolt.MaxReadTransactionOps, func(reader bolt.Reader) error {
		bytes := 0
		for _, candidate := range candidates {
			edition, summary, live, err := s.captureLiveReader(reader, candidate.id)
			if err != nil {
				return err
			}
			if !live {
				continue
			}
			first := int64(0)
			if candidate.at.Equal(request.BeforeCapturedAt) && candidate.id == cursorGroup {
				first = cursorPart + 1
			}
			for index := first; index < summary.PartCount(); index++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				if index >= edition.IndexPart {
					pending = true
					more = true
					break
				}
				if len(items) >= request.Limit {
					more = true
					return nil
				}
				id := capturePartSourceID(candidate.id, edition.Publication, index)
				e, _, found, err := s.sourceEvidenceReader(reader, id)
				if err != nil {
					return err
				}
				if !found {
					return ErrCorrupt
				}
				size := len(e.Text()) + len(e.AppID()) + len(e.Domain())
				if size > request.MaxBytes-bytes {
					more = true
					return nil
				}
				items = append(items, sourceWindowItem{e, candidate.at, candidate.id, index})
				bytes += size
			}
		}
		return nil
	})
	return items, pending, more, err
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
