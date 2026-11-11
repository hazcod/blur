package durable

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const (
	activityNamespace       = "activity"
	activitySourceNamespace = "activity-src"
	maxActivityIDs          = 64
	maxSegmentsPerSource    = 64
	maxActivityRecordBytes  = 16 << 10
)

// PutActivitySegment persists a source-linked time segment. Adjacent compatible
// intervals are coalesced into one durable record to bound reverse-index growth.
// IDs, source references, labels, and timestamps are encrypted and keyed on disk.
func (s *Store) PutActivitySegment(ctx context.Context, segment activity.Segment) error {
	if ctx == nil || segment.Validate() != nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	encoded, err := json.Marshal(segment)
	if err != nil || len(encoded) > maxActivityRecordBytes {
		clear(encoded)
		return &storage.StoreError{Code: storage.Invalid}
	}
	defer clear(encoded)
	return s.Update(ctx, func(tx *Tx) error {
		key := s.logicalKey(groupRecord, kindIndex, "index:"+activityNamespace, segment.ID)
		defer clear(key)
		found, err := tx.writer.Has(key)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		if found {
			return &storage.StoreError{Code: storage.Duplicate}
		}
		merged, err := tx.coalesceActivitySegment(segment)
		if err != nil {
			return err
		}
		if merged {
			return nil
		}
		stored := encoded
		kept := make([]string, 0, len(segment.SourceIDs))
		for _, sourceID := range segment.SourceIDs {
			linked, err := tx.trackActivitySource(sourceID, segment.ID)
			if err != nil {
				return err
			}
			if linked {
				kept = append(kept, sourceID)
			}
		}
		if len(kept) != len(segment.SourceIDs) {
			// A source already cited by maxSegmentsPerSource segments (for
			// example a static screen seen across many sessions) must not make
			// the write fail forever. Persist the time accounting without that
			// content link; such a segment holds no content to cascade-delete.
			unlinked := segment
			unlinked.SourceIDs = kept
			stored, err = json.Marshal(unlinked)
			if err != nil || len(stored) > maxActivityRecordBytes {
				clear(stored)
				return &storage.StoreError{Code: storage.Invalid}
			}
			defer clear(stored)
			segment = unlinked
		}
		if err := tx.addActivityDay(segment); err != nil {
			return err
		}
		return tx.PutIndexRecord(activityNamespace, segment.ID, stored)
	})
}

// coalesceActivitySegment keeps repeated identical observations from creating
// an unbounded source-to-segment reverse index. The older record keeps its ID;
// merge only adjacent, same-day intervals with identical attribution.
func (tx *Tx) coalesceActivitySegment(next activity.Segment) (bool, error) {
	if tx == nil || len(next.SourceIDs) != 1 {
		return false, nil
	}
	encoded, found, err := tx.readIndexValue(activitySourceNamespace, next.SourceIDs[0], 8192)
	if err != nil || !found {
		return false, err
	}
	var ids []string
	if json.Unmarshal(encoded, &ids) != nil || len(ids) > maxSegmentsPerSource {
		clear(encoded)
		return false, ErrCorrupt
	}
	clear(encoded)
	for i, id := range ids {
		if !validOpaqueID(id) || i > 0 && ids[i-1] >= id {
			return false, ErrCorrupt
		}
		priorBytes, found, err := tx.readIndexValue(activityNamespace, id, maxActivityRecordBytes)
		if err != nil {
			return false, err
		}
		if !found {
			return false, ErrCorrupt
		}
		var prior activity.Segment
		if json.Unmarshal(priorBytes, &prior) != nil || prior.ID != id || prior.Validate() != nil {
			clear(priorBytes)
			return false, ErrCorrupt
		}
		clear(priorBytes)
		if !prior.WallEnd.Equal(next.WallStart) || activityUTCDate(prior) != activityUTCDate(next) ||
			prior.AppID != next.AppID || prior.Domain != next.Domain || prior.Project != next.Project ||
			prior.Topic != next.Topic || prior.RuleID != next.RuleID || prior.RuleVersion != next.RuleVersion ||
			len(prior.SourceIDs) != 1 || prior.SourceIDs[0] != next.SourceIDs[0] ||
			prior.Duration > int64(24*time.Hour)-next.Duration {
			continue
		}
		prior.WallEnd = next.WallEnd
		prior.Duration += next.Duration
		merged, err := json.Marshal(prior)
		if err != nil || len(merged) > maxActivityRecordBytes {
			clear(merged)
			return false, &storage.StoreError{Code: storage.Invalid}
		}
		defer clear(merged)
		if err := tx.PutIndexRecord(activityNamespace, prior.ID, merged); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (tx *Tx) trackActivitySource(sourceID, segmentID string) (bool, error) {
	encoded, found, err := tx.readIndexValue(activitySourceNamespace, sourceID, 8192)
	if err != nil {
		return false, err
	}
	var ids []string
	if found {
		if json.Unmarshal(encoded, &ids) != nil || len(ids) > maxSegmentsPerSource {
			clear(encoded)
			return false, ErrCorrupt
		}
		clear(encoded)
	}
	index := sort.SearchStrings(ids, segmentID)
	if index < len(ids) && ids[index] == segmentID {
		return false, ErrCorrupt
	}
	if len(ids) >= maxSegmentsPerSource {
		return false, nil
	}
	ids = append(ids, "")
	copy(ids[index+1:], ids[index:])
	ids[index] = segmentID
	encoded, err = json.Marshal(ids)
	if err != nil {
		return false, ErrCorrupt
	}
	defer clear(encoded)
	if err := tx.PutIndexRecord(activitySourceNamespace, sourceID, encoded); err != nil {
		return false, err
	}
	return true, nil
}

func (tx *Tx) removeActivitySource(sourceID string) error {
	encoded, found, err := tx.readIndexValue(activitySourceNamespace, sourceID, 8192)
	if err != nil || !found {
		return err
	}
	var ids []string
	if json.Unmarshal(encoded, &ids) != nil || len(ids) > maxSegmentsPerSource {
		clear(encoded)
		return ErrCorrupt
	}
	clear(encoded)
	for i, id := range ids {
		if !validOpaqueID(id) || i > 0 && ids[i-1] >= id {
			return ErrCorrupt
		}
		segmentBytes, found, err := tx.readIndexValue(activityNamespace, id, maxActivityRecordBytes)
		if err != nil {
			return err
		}
		if !found {
			return ErrCorrupt
		}
		var segment activity.Segment
		if json.Unmarshal(segmentBytes, &segment) != nil || segment.ID != id || segment.Validate() != nil {
			clear(segmentBytes)
			return ErrCorrupt
		}
		clear(segmentBytes)
		if err := tx.removeActivityDay(segment); err != nil {
			return err
		}
		for _, otherSource := range segment.SourceIDs {
			if otherSource != sourceID {
				if err := tx.untrackActivitySource(otherSource, id); err != nil {
					return err
				}
			}
		}
		if err := tx.deleteKnowledgeRecord(activityNamespace, id); err != nil {
			return err
		}
	}
	return tx.deleteKnowledgeRecord(activitySourceNamespace, sourceID)
}

func (tx *Tx) untrackActivitySource(sourceID, segmentID string) error {
	encoded, found, err := tx.readIndexValue(activitySourceNamespace, sourceID, 8192)
	if err != nil || !found {
		return err
	}
	var ids []string
	if json.Unmarshal(encoded, &ids) != nil || len(ids) > maxSegmentsPerSource {
		clear(encoded)
		return ErrCorrupt
	}
	clear(encoded)
	for i, id := range ids {
		if !validOpaqueID(id) || i > 0 && ids[i-1] >= id {
			return ErrCorrupt
		}
	}
	index := sort.SearchStrings(ids, segmentID)
	if index >= len(ids) || ids[index] != segmentID {
		return ErrCorrupt
	}
	copy(ids[index:], ids[index+1:])
	ids = ids[:len(ids)-1]
	if len(ids) == 0 {
		return tx.deleteKnowledgeRecord(activitySourceNamespace, sourceID)
	}
	encoded, err = json.Marshal(ids)
	if err != nil {
		return ErrCorrupt
	}
	defer clear(encoded)
	return tx.PutIndexRecord(activitySourceNamespace, sourceID, encoded)
}

// GetActivitySegments reads only caller-selected IDs and is bounded to 64
// records/1 MiB. It preserves input order and omits IDs that do not exist.
func (s *Store) GetActivitySegments(ctx context.Context, ids []string) ([]activity.Segment, error) {
	if ctx == nil || len(ids) > maxActivityIDs {
		return nil, &storage.StoreError{Code: storage.Invalid}
	}
	if s == nil || s.state == nil {
		return nil, &storage.StoreError{Code: storage.Closed}
	}
	for _, id := range ids {
		if !validOpaqueID(id) {
			return nil, &storage.StoreError{Code: storage.Invalid}
		}
	}
	out := make([]activity.Segment, 0, len(ids))
	used := 0
	for _, id := range ids {
		var segment activity.Segment
		found, err := s.readActivitySegment(ctx, id, &segment)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		encoded, err := json.Marshal(segment)
		if err != nil {
			return nil, ErrCorrupt
		}
		used += len(encoded)
		clear(encoded)
		if used > 1<<20 {
			return nil, &storage.StoreError{Code: storage.Invalid}
		}
		out = append(out, segment)
	}
	return out, nil
}

func (s *Store) readActivitySegment(ctx context.Context, id string, dst *activity.Segment) (bool, error) {
	var found bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		found, err = s.readActivitySegmentReader(reader, id, dst)
		return err
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

func (s *Store) readActivitySegmentReader(reader bolt.Reader, id string, dst *activity.Segment) (bool, error) {
	key := s.logicalKey(groupRecord, kindIndex, "index:"+activityNamespace, id)
	defer clear(key)
	stored, found, err := reader.Get(key)
	if err != nil {
		return false, mapStorePhysicalError(err)
	}
	if !found {
		return false, nil
	}
	if len(stored) > maxActivityRecordBytes+1024 {
		clear(stored)
		return false, ErrCorrupt
	}
	plain, err := s.open(key, "index:"+activityNamespace, stored)
	clear(stored)
	if err != nil {
		return false, ErrCorrupt
	}
	defer clear(plain)
	if len(plain) < 5 || plain[0] != valueVersion || binary.BigEndian.Uint32(plain[1:5]) != uint32(len(plain)-5) || len(plain)-5 > maxActivityRecordBytes {
		return false, ErrCorrupt
	}
	if err := json.Unmarshal(plain[5:], dst); err != nil || dst.Validate() != nil || dst.ID != id {
		return false, ErrCorrupt
	}
	return true, nil
}
