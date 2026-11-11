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
	maxActivityDayPages      = 64
	maxActivityDaySegments   = activityDayPageSize * maxActivityDayPages
	maxActivityRangeDates    = 33
	maxActivityRangeSegments = activity.MaxReportSegments
	maxActivityRangeBytes    = 8 << 20
)

type activityDayMeta struct {
	Pages int `json:"pages"`
	Count int `json:"count"`
}

// ListActivitySegmentsForDate pages the segments whose WallStart falls on the
// requested UTC date, in insertion order. The cursor is the last returned
// segment ID; pages are limited to 64 records.
func (s *Store) ListActivitySegmentsForDate(ctx context.Context, date, after string, limit int) ([]activity.Segment, string, error) {
	if ctx == nil || !validTodayDate(date) || limit < 1 || limit > maxActivityIDs || after != "" && !validOpaqueID(after) {
		return nil, "", &storage.StoreError{Code: storage.Invalid}
	}
	if s == nil || s.state == nil {
		return nil, "", &storage.StoreError{Code: storage.Closed}
	}
	var segments []activity.Segment
	var next string
	err := s.view(ctx, func(reader bolt.Reader) error {
		metaBytes, found, err := s.readIndexValueReader(reader, activityDayNamespace, date, 128)
		if err != nil || !found {
			return err
		}
		var meta activityDayMeta
		if json.Unmarshal(metaBytes, &meta) != nil || meta.Pages < 1 || meta.Pages > maxActivityDayPages || meta.Count < 1 || meta.Count > maxActivityDaySegments {
			clear(metaBytes)
			return ErrCorrupt
		}
		clear(metaBytes)
		foundCursor := after == ""
		selected := make([]string, 0, limit+1)
		seenIDs := make(map[string]struct{}, meta.Count)
		for page := 0; page < meta.Pages && len(selected) <= limit; page++ {
			pageID := activityDayPageID(date, page)
			pageBytes, exists, err := s.readIndexValueReader(reader, activityDayNamespace, pageID, 8192)
			if err != nil {
				return err
			}
			if !exists {
				return ErrCorrupt
			}
			var ids []string
			if json.Unmarshal(pageBytes, &ids) != nil || !validActivityDayIDs(ids) {
				clear(pageBytes)
				return ErrCorrupt
			}
			clear(pageBytes)
			for _, id := range ids {
				if _, duplicate := seenIDs[id]; duplicate {
					return ErrCorrupt
				}
				seenIDs[id] = struct{}{}
				if !foundCursor {
					if id == after {
						foundCursor = true
					}
					continue
				}
				selected = append(selected, id)
				if len(selected) > limit {
					break
				}
			}
		}
		if !foundCursor {
			return &storage.StoreError{Code: storage.Invalid}
		}
		more := len(selected) > limit
		if more {
			selected = selected[:limit]
		}
		segments = make([]activity.Segment, 0, len(selected))
		used := 0
		for _, id := range selected {
			var segment activity.Segment
			found, err := s.readActivitySegmentReader(reader, id, &segment)
			if err != nil || !found {
				return ErrCorrupt
			}
			if activityUTCDate(segment) != date {
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
		if more && len(selected) > 0 {
			next = selected[len(selected)-1]
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
	if s == nil || s.state == nil {
		return nil, &storage.StoreError{Code: storage.Closed}
	}
	segments := make([]activity.Segment, 0)
	err := s.view(ctx, func(reader bolt.Reader) error {
		seen := make(map[string]struct{})
		used := 0
		for _, date := range dates {
			metaBytes, found, err := s.readIndexValueReader(reader, activityDayNamespace, date, 128)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			var meta activityDayMeta
			if json.Unmarshal(metaBytes, &meta) != nil || meta.Pages < 1 || meta.Pages > maxActivityDayPages || meta.Count < 1 || meta.Count > maxActivityDaySegments {
				clear(metaBytes)
				return ErrCorrupt
			}
			clear(metaBytes)
			var dayIDs []string
			for page := 0; page < meta.Pages; page++ {
				pageBytes, exists, err := s.readIndexValueReader(reader, activityDayNamespace, activityDayPageID(date, page), 8192)
				if err != nil {
					return err
				}
				if !exists {
					return ErrCorrupt
				}
				var ids []string
				if json.Unmarshal(pageBytes, &ids) != nil || !validActivityDayIDs(ids) || len(ids) == 0 {
					clear(pageBytes)
					return ErrCorrupt
				}
				clear(pageBytes)
				dayIDs = append(dayIDs, ids...)
			}
			if len(dayIDs) != meta.Count {
				return ErrCorrupt
			}
			for _, id := range dayIDs {
				if _, duplicate := seen[id]; duplicate {
					return ErrCorrupt
				}
				seen[id] = struct{}{}
				if len(segments) >= limit {
					return &storage.StoreError{Code: storage.Invalid}
				}
				var segment activity.Segment
				ok, err := s.readActivitySegmentReader(reader, id, &segment)
				if err != nil || !ok || activityUTCDate(segment) != date || segment.Validate() != nil {
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
	metaBytes, found, err := tx.readIndexValue(activityDayNamespace, date, 128)
	if err != nil {
		return err
	}
	meta := activityDayMeta{}
	if found {
		if json.Unmarshal(metaBytes, &meta) != nil || meta.Pages < 1 || meta.Pages > maxActivityDayPages || meta.Count < 1 || meta.Count > maxActivityDaySegments {
			clear(metaBytes)
			return ErrCorrupt
		}
	}
	if found && meta.Count == maxActivityDaySegments {
		return &storage.StoreError{Code: storage.Invalid}
	}
	clear(metaBytes)
	page := 0
	if meta.Pages == 0 {
		meta.Pages = 1
	} else {
		page = meta.Pages - 1
	}
	pageID := activityDayPageID(date, page)
	pageBytes, pageFound, err := tx.readIndexValue(activityDayNamespace, pageID, 8192)
	if err != nil {
		return err
	}
	var ids []string
	if pageFound {
		if json.Unmarshal(pageBytes, &ids) != nil || !validActivityDayIDs(ids) {
			clear(pageBytes)
			return ErrCorrupt
		}
	}
	if found && !pageFound {
		return ErrCorrupt
	}
	clear(pageBytes)
	if len(ids) == activityDayPageSize {
		if meta.Pages >= maxActivityDayPages {
			return &storage.StoreError{Code: storage.Invalid}
		}
		page = meta.Pages
		meta.Pages++
		pageID = activityDayPageID(date, page)
		ids = nil
	}
	ids = append(ids, segment.ID)
	encoded, err := json.Marshal(ids)
	if err != nil {
		return ErrCorrupt
	}
	if err := tx.PutIndexRecord(activityDayNamespace, pageID, encoded); err != nil {
		clear(encoded)
		return err
	}
	clear(encoded)
	if err := tx.PutIndexRecord(activityDayRefNamespace, segment.ID, []byte(pageID)); err != nil {
		return err
	}
	meta.Count++
	encoded, err = json.Marshal(meta)
	if err != nil {
		return ErrCorrupt
	}
	defer clear(encoded)
	return tx.PutIndexRecord(activityDayNamespace, date, encoded)
}

func (tx *Tx) removeActivityDay(segment activity.Segment) error {
	refBytes, found, err := tx.readIndexValue(activityDayRefNamespace, segment.ID, 64)
	if err != nil || !found {
		return ErrCorrupt
	}
	pageID := string(refBytes)
	clear(refBytes)
	date := activityUTCDate(segment)
	pageNumber := -1
	for i := 0; i < maxActivityDayPages; i++ {
		if activityDayPageID(date, i) == pageID {
			pageNumber = i
			break
		}
	}
	if pageNumber < 0 {
		return ErrCorrupt
	}
	pageBytes, found, err := tx.readIndexValue(activityDayNamespace, pageID, 8192)
	if err != nil || !found {
		return ErrCorrupt
	}
	var ids []string
	if json.Unmarshal(pageBytes, &ids) != nil || !validActivityDayIDs(ids) {
		clear(pageBytes)
		return ErrCorrupt
	}
	clear(pageBytes)
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
	copy(ids[index:], ids[index+1:])
	ids = ids[:len(ids)-1]
	metaBytes, found, err := tx.readIndexValue(activityDayNamespace, date, 128)
	if err != nil || !found {
		return ErrCorrupt
	}
	var meta activityDayMeta
	if json.Unmarshal(metaBytes, &meta) != nil || meta.Pages < 1 || meta.Pages > maxActivityDayPages || meta.Count < 1 || pageNumber >= meta.Pages {
		clear(metaBytes)
		return ErrCorrupt
	}
	clear(metaBytes)
	meta.Count--
	if meta.Count == 0 {
		for page := 0; page < meta.Pages; page++ {
			if err := tx.deleteKnowledgeRecord(activityDayNamespace, activityDayPageID(date, page)); err != nil {
				return err
			}
		}
		if err := tx.deleteKnowledgeRecord(activityDayNamespace, date); err != nil {
			return err
		}
	} else {
		encoded, err := json.Marshal(ids)
		if err != nil {
			return ErrCorrupt
		}
		if err := tx.PutIndexRecord(activityDayNamespace, pageID, encoded); err != nil {
			clear(encoded)
			return err
		}
		clear(encoded)
		encoded, err = json.Marshal(meta)
		if err != nil {
			return ErrCorrupt
		}
		if err := tx.PutIndexRecord(activityDayNamespace, date, encoded); err != nil {
			clear(encoded)
			return err
		}
		clear(encoded)
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
