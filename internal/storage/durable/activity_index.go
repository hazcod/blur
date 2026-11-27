package durable

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/storage/recordcodec"
)

const (
	activityDayNamespace     = "activity-day"
	activityDayRefNamespace  = "activity-day-ref"
	activityDayPageSize      = 64
	maxActivityRangeDates    = 33
	maxActivityRangeSegments = activity.MaxReportSegments
	maxActivityRangeBytes    = 8 << 20
)

type activityDayMeta struct {
	Version int `json:"version,omitempty"`
	Pages   int `json:"pages"`
	Count   int `json:"count"`
	First   int `json:"first,omitempty"`
	Last    int `json:"last,omitempty"`
}

// ListActivitySegmentsForDate pages the segments whose WallStart falls on the
// requested UTC date, in insertion order. The cursor is the last returned
// segment ID; pages are limited to 64 records.
func (s *Store) ListActivitySegmentsForDate(ctx context.Context, date, after string, limit int) ([]activity.Segment, string, error) {
	if ctx == nil || !validTodayDate(date) || limit < 1 || limit > maxActivityIDs || after != "" && !validOpaqueID(after) {
		return nil, "", &storage.StoreError{Code: storage.Invalid}
	}
	var segments []activity.Segment
	var next string
	err := s.view(ctx, func(reader bolt.Reader) error {
		encoded, found, err := s.readIndexValueReader(reader, activityDayNamespace, date, 256)
		if err != nil || !found {
			return err
		}
		meta, err := decodeActivityDayMeta(encoded)
		clear(encoded)
		if err != nil {
			return err
		}
		ids, err := s.activityDayIDs(reader, date, after, meta, limit+1)
		if err != nil {
			return err
		}
		more := len(ids) > limit
		if more {
			ids = ids[:limit]
		}
		segments = make([]activity.Segment, 0, len(ids))
		used := 0
		for _, id := range ids {
			var segment activity.Segment
			found, err := s.readActivitySegmentReader(reader, id, &segment)
			if err != nil {
				return err
			}
			if !found || activityUTCDate(segment) != date {
				return ErrCorrupt
			}
			encoded, err := json.Marshal(segment)
			if err != nil {
				return ErrCorrupt
			}
			used += len(encoded)
			clear(encoded)
			if used > 1<<20 {
				return &storage.StoreError{Code: storage.Invalid}
			}
			segments = append(segments, segment)
		}
		if more && len(ids) > 0 {
			next = ids[len(ids)-1]
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return segments, next, nil
}

// ListActivitySegmentsForDates reads a sorted set of UTC-day partitions from
// one Bolt snapshot. Both the record count and decoded aggregate size are
// bounded; it is intended for report windows, not unbounded export.
func (s *Store) ListActivitySegmentsForDates(ctx context.Context, dates []string, limit int) ([]activity.Segment, error) {
	if ctx == nil || len(dates) == 0 || len(dates) > maxActivityRangeDates || limit < 1 || limit > maxActivityRangeSegments {
		return nil, &storage.StoreError{Code: storage.Invalid}
	}
	for i, date := range dates {
		if !validTodayDate(date) || i > 0 && dates[i-1] >= date {
			return nil, &storage.StoreError{Code: storage.Invalid}
		}
	}
	segments := make([]activity.Segment, 0)
	// Reports retain a separate bounded read budget; larger accepted days
	// remain available through ListActivitySegmentsForDate pagination.
	err := s.viewWithReadLimit(ctx, bolt.MaxReadTransactionOps, func(reader bolt.Reader) error {
		seen := make(map[string]bool)
		used := 0
		for _, date := range dates {
			encoded, found, err := s.readIndexValueReader(reader, activityDayNamespace, date, 256)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			meta, err := decodeActivityDayMeta(encoded)
			clear(encoded)
			if err != nil {
				return err
			}
			if meta.Count > limit-len(segments) {
				return &storage.StoreError{Code: storage.Invalid}
			}
			ids, err := s.activityDayIDs(reader, date, "", meta, meta.Count+1)
			if err != nil {
				return err
			}
			if len(ids) != meta.Count {
				return ErrCorrupt
			}
			for _, id := range ids {
				if seen[id] {
					return ErrCorrupt
				}
				seen[id] = true
				var segment activity.Segment
				found, err := s.readActivitySegmentReader(reader, id, &segment)
				if err != nil {
					return err
				}
				if !found || activityUTCDate(segment) != date {
					return ErrCorrupt
				}
				encoded, err := json.Marshal(segment)
				if err != nil {
					return ErrCorrupt
				}
				used += len(encoded)
				clear(encoded)
				if used > maxActivityRangeBytes {
					return &storage.StoreError{Code: storage.Invalid}
				}
				segments = append(segments, segment)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return segments, nil
}

func activityDayPageID(date string, page int) string {
	return fmt.Sprintf("%s:p:%02d", date, page)
}

func activityUTCDate(segment activity.Segment) string {
	return segment.WallStart.UTC().Format("2006-01-02")
}

func validActivityDayIDs(ids []string) bool {
	if len(ids) > activityDayPageSize {
		return false
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !validOpaqueID(id) {
			return false
		}
		if _, ok := seen[id]; ok {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func (tx *Tx) addActivityDay(segment activity.Segment) error {
	date := activityUTCDate(segment)
	encoded, found, err := tx.readIndexValue(activityDayNamespace, date, 256)
	if err != nil {
		return err
	}
	meta := activityDayMeta{Version: 2, Pages: 1}
	if found {
		meta, err = decodeActivityDayMeta(encoded)
		clear(encoded)
		if err != nil {
			return err
		}
		meta, err = tx.migrateActivityDay(date, meta)
		if err != nil {
			return err
		}
	} else {
		if err := tx.PutIndexRecord(activityDayLinksNamespace, activityDayPageID(date, 0), mustJSON(activityDayLink{Before: -1, After: -1})); err != nil {
			return err
		}
	}
	page := meta.Last
	pageID := activityDayPageID(date, page)
	pageBytes, pageFound, err := tx.readIndexValue(activityDayNamespace, pageID, 8192)
	if err != nil {
		return err
	}
	var ids []string
	if pageFound && (json.Unmarshal(pageBytes, &ids) != nil || !validActivityDayIDs(ids) || len(ids) == 0) {
		clear(pageBytes)
		return ErrCorrupt
	}
	clear(pageBytes)
	if found && !pageFound {
		return ErrCorrupt
	}
	if len(ids) == activityDayPageSize {
		oldLink, err := tx.activityDayLink(date, page, meta)
		if err != nil || oldLink.After != -1 {
			if err != nil {
				return err
			}
			return ErrCorrupt
		}
		page = meta.Pages
		if page == int(^uint(0)>>1) {
			return ErrCorrupt
		}
		meta.Pages++
		oldLink.After = page
		if err := tx.PutIndexRecord(activityDayLinksNamespace, pageID, mustJSON(oldLink)); err != nil {
			return err
		}
		pageID = activityDayPageID(date, page)
		if err := tx.PutIndexRecord(activityDayLinksNamespace, pageID, mustJSON(activityDayLink{Before: meta.Last, After: -1})); err != nil {
			return err
		}
		meta.Last = page
		ids = nil
	}
	if meta.Count == int(^uint(0)>>1) {
		return ErrCorrupt
	}
	ids = append(ids, segment.ID)
	if err := tx.PutIndexRecord(activityDayNamespace, pageID, mustJSON(ids)); err != nil {
		return err
	}
	if err := tx.PutIndexRecord(activityDayRefNamespace, segment.ID, []byte(pageID)); err != nil {
		return err
	}
	meta.Count++
	return tx.PutIndexRecord(activityDayNamespace, date, mustJSON(meta))
}

func (tx *Tx) removeActivityDay(segment activity.Segment) error {
	ref, found, err := tx.readIndexValue(activityDayRefNamespace, segment.ID, 64)
	if err != nil {
		return err
	}
	if !found {
		return ErrCorrupt
	}
	pageID := string(ref)
	clear(ref)
	date := activityUTCDate(segment)
	page, err := parseActivityDayPageID(date, pageID)
	if err != nil {
		return err
	}
	encoded, found, err := tx.readIndexValue(activityDayNamespace, date, 256)
	if err != nil {
		return err
	}
	if !found {
		return ErrCorrupt
	}
	meta, err := decodeActivityDayMeta(encoded)
	clear(encoded)
	if err != nil || page >= meta.Pages {
		return ErrCorrupt
	}
	meta, err = tx.migrateActivityDay(date, meta)
	if err != nil {
		return err
	}
	encoded, found, err = tx.readIndexValue(activityDayNamespace, pageID, 8192)
	if err != nil {
		return err
	}
	var ids []string
	valid := found && json.Unmarshal(encoded, &ids) == nil && validActivityDayIDs(ids) && len(ids) > 0
	clear(encoded)
	if !valid {
		return ErrCorrupt
	}
	index := -1
	for i, id := range ids {
		if id == segment.ID {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrCorrupt
	}
	ids = append(ids[:index], ids[index+1:]...)
	meta.Count--
	if len(ids) == 0 {
		link, err := tx.activityDayLink(date, page, meta)
		if err != nil {
			return err
		}
		if link.Before == -1 {
			if meta.First != page {
				return ErrCorrupt
			}
			meta.First = link.After
		} else {
			prior, err := tx.activityDayLink(date, link.Before, meta)
			if err != nil || prior.After != page {
				if err != nil {
					return err
				}
				return ErrCorrupt
			}
			prior.After = link.After
			if err := tx.PutIndexRecord(activityDayLinksNamespace, activityDayPageID(date, link.Before), mustJSON(prior)); err != nil {
				return err
			}
		}
		if link.After == -1 {
			if meta.Last != page {
				return ErrCorrupt
			}
			meta.Last = link.Before
		} else {
			next, err := tx.activityDayLink(date, link.After, meta)
			if err != nil || next.Before != page {
				if err != nil {
					return err
				}
				return ErrCorrupt
			}
			next.Before = link.Before
			if err := tx.PutIndexRecord(activityDayLinksNamespace, activityDayPageID(date, link.After), mustJSON(next)); err != nil {
				return err
			}
		}
		if err := tx.deleteKnowledgeRecord(activityDayNamespace, pageID); err != nil {
			return err
		}
		if err := tx.deleteKnowledgeRecord(activityDayLinksNamespace, pageID); err != nil {
			return err
		}
	} else if err := tx.PutIndexRecord(activityDayNamespace, pageID, mustJSON(ids)); err != nil {
		return err
	}
	if meta.Count == 0 {
		if meta.First != -1 || meta.Last != -1 {
			return ErrCorrupt
		}
		if err := tx.deleteKnowledgeRecord(activityDayNamespace, date); err != nil {
			return err
		}
	} else if err := tx.PutIndexRecord(activityDayNamespace, date, mustJSON(meta)); err != nil {
		return err
	}
	return tx.deleteKnowledgeRecord(activityDayRefNamespace, segment.ID)
}

func (s *Store) readIndexValue(ctx context.Context, namespace, id string, maxBytes int) ([]byte, bool, error) {
	if s == nil || s.state == nil {
		return nil, false, &storage.StoreError{Code: storage.Closed}
	}
	if ctx == nil || !validNamespace(namespace) || !validOpaqueID(id) || maxBytes < 1 || maxBytes > recordcodec.MaxPlaintextBytes-5 {
		return nil, false, &storage.StoreError{Code: storage.Invalid}
	}
	var result []byte
	found := false
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		result, found, err = s.readIndexValueReader(reader, namespace, id, maxBytes)
		return err
	})
	if err != nil {
		clear(result)
		return nil, false, err
	}
	return result, found, nil
}

func (s *Store) readIndexValueReader(reader bolt.Reader, namespace, id string, maxBytes int) ([]byte, bool, error) {
	key := s.logicalKey(groupRecord, kindIndex, "index:"+namespace, id)
	defer clear(key)
	stored, found, err := reader.Get(key)
	if err != nil {
		return nil, false, mapStorePhysicalError(err)
	}
	if !found {
		return nil, false, nil
	}
	if len(stored) > maxBytes+1024 {
		clear(stored)
		return nil, false, ErrCorrupt
	}
	plain, err := s.open(key, "index:"+namespace, stored)
	clear(stored)
	if err != nil {
		return nil, false, ErrCorrupt
	}
	defer clear(plain)
	if len(plain) < 5 || plain[0] != valueVersion || int(binary.BigEndian.Uint32(plain[1:5])) != len(plain)-5 || len(plain)-5 > maxBytes {
		return nil, false, ErrCorrupt
	}
	return append([]byte(nil), plain[5:]...), true, nil
}
