package durable

import (
	"context"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"math"
	"time"
)

func (s *Store) captureVisitReader(reader bolt.Reader, id string, page int64) (captureVisitRecord, bool, error) {
	var r captureVisitRecord
	found, err := s.captureReadJSON(reader, captureVisitKey(id, page), captureMetaBytes, &r)
	if err != nil || !found {
		return r, found, err
	}
	if r.Version != 1 || len(r.Rows) == 0 || len(r.Rows) > captureVisitRows {
		return r, false, ErrCorrupt
	}
	seen := map[string]bool{}
	for _, row := range r.Rows {
		if !validHash(row.CaptureID) || seen[row.CaptureID] {
			return r, false, ErrCorrupt
		}
		seen[row.CaptureID] = true
	}
	return r, true, nil
}
func (tx *Tx) captureAppendVisitLocked(id string, header capture.CaptureHeader, edition *captureEditionRecord) error {
	if edition.Visits == math.MaxInt64 {
		return ErrCorrupt
	}
	pageIndex, slot := edition.Visits/captureVisitRows, int(edition.Visits%captureVisitRows)
	page, found, err := tx.store.captureVisitReader(tx.writer, id, pageIndex)
	if err != nil {
		return err
	}
	if !found {
		if slot != 0 {
			return ErrCorrupt
		}
		page = captureVisitRecord{Version: 1}
	}
	if len(page.Rows) != slot {
		return ErrCorrupt
	}
	page.Rows = append(page.Rows, captureVisitRow{CaptureID: header.CaptureID(), At: header.CapturedAt().UnixNano()})
	receipt := captureReceipt{Version: 1, EditionID: id, FamilyID: header.FamilyID(), At: header.CapturedAt().UnixNano(), Index: edition.Visits}
	if err := tx.capturePutJSONLocked(captureVisitKey(id, pageIndex), page, captureMetaBytes); err != nil {
		return err
	}
	if err := tx.capturePutJSONLocked("receipt:"+header.CaptureID(), receipt, captureMetaBytes); err != nil {
		return err
	}
	if err := tx.indexCaptureVisitLocked(id, edition.Publication, header.CaptureID(), header.CapturedAt().UnixNano()); err != nil {
		return err
	}
	if edition.IndexVersion == captureIndexVersion && edition.IndexVisits == edition.Visits {
		edition.IndexVisits++
	}
	edition.Visits++
	edition.LastSeen = max(edition.LastSeen, header.CapturedAt().UnixNano())
	return nil
}
func (s *Store) ListCaptureVisits(ctx context.Context, id, after string, limit int) (CaptureVisitPage, error) {
	page := CaptureVisitPage{Next: after}
	if !validHash(id) || !captureOptionalID(after) || limit < 1 || limit > captureVisitRows {
		return page, &storage.StoreError{Code: storage.Invalid}
	}
	err := s.view(ctx, func(reader bolt.Reader) error {
		r, summary, found, err := s.captureLiveReader(reader, id)
		if err != nil {
			return err
		}
		if !found {
			return ErrMissing
		}
		index := int64(0)
		if after != "" {
			receipt, found, err := s.captureReceiptReader(reader, after)
			if err != nil {
				return err
			}
			if !found || receipt.EditionID != id || receipt.Index >= r.Visits {
				return ErrCaptureCursorChanged
			}
			index = receipt.Index + 1
		}
		loaded := int64(-1)
		var records captureVisitRecord
		for ; index < r.Visits && len(page.Visits) < limit; index++ {
			p := index / captureVisitRows
			if p != loaded {
				var found bool
				records, found, err = s.captureVisitReader(reader, id, p)
				if err != nil {
					return err
				}
				if !found {
					return ErrCorrupt
				}
				loaded = p
			}
			slot := int(index % captureVisitRows)
			if slot >= len(records.Rows) {
				return ErrCorrupt
			}
			row := records.Rows[slot]
			receipt, found, err := s.captureReceiptReader(reader, row.CaptureID)
			if err != nil {
				return err
			}
			if !found || receipt.EditionID != id || receipt.FamilyID != summary.Header().FamilyID() || receipt.At != row.At || receipt.Index != index {
				return ErrCorrupt
			}
			page.Visits = append(page.Visits, CaptureVisit{CaptureID: row.CaptureID, CapturedAt: time.Unix(0, row.At).UTC()})
			page.Next = row.CaptureID
		}
		page.HasMore = index < r.Visits
		return nil
	})
	if err != nil {
		return CaptureVisitPage{}, err
	}
	return page, nil
}
