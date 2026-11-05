package durable

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"time"

	"github.com/nielsgroen/blur/internal/storage"
	"github.com/nielsgroen/blur/internal/storage/physical/bolt"
	"github.com/nielsgroen/blur/internal/today"
)

const todayNamespace = "today"

// PutTodaySnapshot atomically replaces the encrypted, per-local-date
// materialized snapshot. The generation ID and source IDs are encrypted with
// the snapshot; readers never scan the activity or observation stores.
func (s *Store) PutTodaySnapshot(ctx context.Context, snapshot today.Snapshot) error {
	if ctx == nil || snapshot.Validate() != nil || !validTodayDate(snapshot.Date) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) > today.MaxSnapshotBytes {
		clear(encoded)
		return &storage.StoreError{Code: storage.Invalid}
	}
	defer clear(encoded)
	return s.Update(ctx, func(tx *Tx) error {
		return tx.PutIndexRecord(todayNamespace, snapshot.Date, encoded)
	})
}

// GetTodaySnapshot performs one bounded lookup of the fixed local-date key.
// It does not materialize, scan, tokenize, or call any model/provider.
func (s *Store) GetTodaySnapshot(ctx context.Context, date string) (today.Snapshot, bool, error) {
	if ctx == nil || !validTodayDate(date) {
		return today.Snapshot{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	if s == nil || s.state == nil {
		return today.Snapshot{}, false, &storage.StoreError{Code: storage.Closed}
	}
	key := s.logicalKey(groupRecord, kindIndex, "index:"+todayNamespace, date)
	defer clear(key)
	var out today.Snapshot
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
		if len(stored) > today.MaxSnapshotBytes+1024 {
			clear(stored)
			return ErrCorrupt
		}
		plain, err := s.open(key, "index:"+todayNamespace, stored)
		clear(stored)
		if err != nil {
			return ErrCorrupt
		}
		defer clear(plain)
		if len(plain) < 5 || plain[0] != valueVersion || binary.BigEndian.Uint32(plain[1:5]) != uint32(len(plain)-5) || len(plain)-5 > today.MaxSnapshotBytes {
			return ErrCorrupt
		}
		if err := json.Unmarshal(plain[5:], &out); err != nil || out.Date != date || out.Validate() != nil {
			return ErrCorrupt
		}
		return nil
	})
	if err != nil {
		return today.Snapshot{}, false, err
	}
	if !found {
		return today.Snapshot{}, false, nil
	}
	return out.Clone(), true, nil
}

func validTodayDate(date string) bool {
	t, err := time.Parse("2006-01-02", date)
	return err == nil && t.Format("2006-01-02") == date
}
