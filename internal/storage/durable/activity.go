package durable

import (
	"context"
	"encoding/binary"
	"encoding/json"

	"github.com/nielsgroen/blur/internal/activity"
	"github.com/nielsgroen/blur/internal/storage"
	"github.com/nielsgroen/blur/internal/storage/physical/bolt"
)

const (
	activityNamespace      = "activity"
	maxActivityIDs         = 64
	maxActivityRecordBytes = 16 << 10
)

// PutActivitySegment stores an immutable, source-linked time segment. Its ID,
// source references, labels, and timestamps are encrypted and keyed on disk.
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
		return tx.PutIndexRecord(activityNamespace, segment.ID, encoded)
	})
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
	key := s.logicalKey(groupRecord, kindIndex, "index:"+activityNamespace, id)
	defer clear(key)
	var found bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		stored, ok, err := reader.Get(key)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		if !ok {
			return nil
		}
		found = true
		if len(stored) > maxActivityRecordBytes+1024 {
			clear(stored)
			return ErrCorrupt
		}
		plain, err := s.open(key, "index:"+activityNamespace, stored)
		clear(stored)
		if err != nil {
			return ErrCorrupt
		}
		defer clear(plain)
		if len(plain) < 5 || plain[0] != valueVersion || binary.BigEndian.Uint32(plain[1:5]) != uint32(len(plain)-5) || len(plain)-5 > maxActivityRecordBytes {
			return ErrCorrupt
		}
		if err := json.Unmarshal(plain[5:], dst); err != nil || dst.Validate() != nil || dst.ID != id {
			return ErrCorrupt
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return found, nil
}
