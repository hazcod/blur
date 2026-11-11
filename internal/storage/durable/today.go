package durable

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/storage/recordcodec"
	"github.com/hazcod/blur/internal/today"
)

const todayNamespace = "today"
const todaySourceNamespace = "today-src"

const maxTodaySourceDates = 64

// PutTodaySnapshot atomically replaces the encrypted, per-local-date
// materialized snapshot. The generation ID and source IDs are encrypted with
// the snapshot; readers never scan the activity or observation stores.
func (s *Store) PutTodaySnapshot(ctx context.Context, snapshot today.Snapshot) error {
	if ctx == nil || snapshot.Validate() != nil || !validTodayDate(snapshot.Date) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	return s.Update(ctx, func(tx *Tx) error { return tx.putTodaySnapshot(snapshot) })
}

func (tx *Tx) putTodaySnapshot(snapshot today.Snapshot) error {
	if tx == nil || snapshot.Validate() != nil || !validTodayDate(snapshot.Date) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	oldBytes, found, err := tx.readIndexValue(todayNamespace, snapshot.Date, today.MaxSnapshotBytes)
	if err != nil {
		return err
	}
	oldSources := make([]string, 0)
	if found {
		var previous today.Snapshot
		if json.Unmarshal(oldBytes, &previous) != nil || previous.Date != snapshot.Date || previous.Validate() != nil {
			clear(oldBytes)
			return ErrCorrupt
		}
		oldSources = previous.SourceIDs
	}
	clear(oldBytes)
	oldSet := make(map[string]struct{}, len(oldSources))
	newSet := make(map[string]struct{}, len(snapshot.SourceIDs))
	for _, id := range oldSources {
		oldSet[id] = struct{}{}
	}
	for _, id := range snapshot.SourceIDs {
		newSet[id] = struct{}{}
	}
	for _, id := range oldSources {
		if _, keep := newSet[id]; !keep {
			if err := tx.updateTodaySourceDate(id, snapshot.Date, false); err != nil {
				return err
			}
		}
	}
	for _, id := range snapshot.SourceIDs {
		if _, already := oldSet[id]; !already {
			if err := tx.updateTodaySourceDate(id, snapshot.Date, true); err != nil {
				return err
			}
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) > today.MaxSnapshotBytes {
		clear(encoded)
		return &storage.StoreError{Code: storage.Invalid}
	}
	defer clear(encoded)
	return tx.PutIndexRecord(todayNamespace, snapshot.Date, encoded)
}

func (tx *Tx) readIndexValue(namespace, id string, maxBytes int) ([]byte, bool, error) {
	if tx == nil || !validNamespace(namespace) || !validOpaqueID(id) || maxBytes < 1 || maxBytes > recordcodec.MaxPlaintextBytes-5 {
		return nil, false, &storage.StoreError{Code: storage.Invalid}
	}
	key := tx.store.logicalKey(groupRecord, kindIndex, "index:"+namespace, id)
	defer clear(key)
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.checkLocked(); err != nil {
		return nil, false, tx.failLocked(err)
	}
	stored, found, err := tx.writer.Get(key)
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
	plain, err := tx.store.open(key, "index:"+namespace, stored)
	clear(stored)
	if err != nil {
		return nil, false, ErrCorrupt
	}
	defer clear(plain)
	if len(plain) < 5 || plain[0] != valueVersion || binary.BigEndian.Uint32(plain[1:5]) != uint32(len(plain)-5) || len(plain)-5 > maxBytes {
		return nil, false, ErrCorrupt
	}
	return append([]byte(nil), plain[5:]...), true, nil
}

func (tx *Tx) updateTodaySourceDate(sourceID, date string, add bool) error {
	if !validOpaqueID(sourceID) || !validTodayDate(date) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	encoded, found, err := tx.readIndexValue(todaySourceNamespace, sourceID, 2048)
	if err != nil {
		return err
	}
	var dates []string
	if found {
		if json.Unmarshal(encoded, &dates) != nil || len(dates) > maxTodaySourceDates {
			clear(encoded)
			return ErrCorrupt
		}
		clear(encoded)
		for i, value := range dates {
			if !validTodayDate(value) || i > 0 && dates[i-1] >= value {
				return ErrCorrupt
			}
		}
	}
	index := sort.SearchStrings(dates, date)
	exists := index < len(dates) && dates[index] == date
	if add && !exists {
		if len(dates) >= maxTodaySourceDates {
			return &storage.StoreError{Code: storage.Invalid}
		}
		dates = append(dates, "")
		copy(dates[index+1:], dates[index:])
		dates[index] = date
	} else if !add && exists {
		copy(dates[index:], dates[index+1:])
		dates = dates[:len(dates)-1]
	}
	if len(dates) == 0 {
		return tx.deleteKnowledgeRecord(todaySourceNamespace, sourceID)
	}
	encoded, err = json.Marshal(dates)
	if err != nil {
		return ErrCorrupt
	}
	defer clear(encoded)
	return tx.PutIndexRecord(todaySourceNamespace, sourceID, encoded)
}

func (tx *Tx) removeTodaySource(sourceID string) error {
	encoded, found, err := tx.readIndexValue(todaySourceNamespace, sourceID, 2048)
	if err != nil || !found {
		return err
	}
	var dates []string
	if json.Unmarshal(encoded, &dates) != nil || len(dates) > maxTodaySourceDates {
		clear(encoded)
		return ErrCorrupt
	}
	clear(encoded)
	for i, date := range dates {
		if !validTodayDate(date) || i > 0 && dates[i-1] >= date {
			return ErrCorrupt
		}
		snapshotBytes, found, err := tx.readIndexValue(todayNamespace, date, today.MaxSnapshotBytes)
		if err != nil {
			return err
		}
		if !found {
			if err := tx.updateTodaySourceDate(sourceID, date, false); err != nil {
				return err
			}
			continue
		}
		var snapshot today.Snapshot
		if json.Unmarshal(snapshotBytes, &snapshot) != nil || snapshot.Date != date || snapshot.Validate() != nil {
			clear(snapshotBytes)
			return ErrCorrupt
		}
		clear(snapshotBytes)
		cleaned, err := today.WithoutSource(snapshot, sourceID)
		if err != nil {
			return ErrCorrupt
		}
		if todaySnapshotEmpty(cleaned) {
			if err := tx.deleteKnowledgeRecord(todayNamespace, date); err != nil {
				return err
			}
			if err := tx.updateTodaySourceDate(sourceID, date, false); err != nil {
				return err
			}
			continue
		}
		if err := tx.putTodaySnapshot(cleaned); err != nil {
			return err
		}
	}
	return nil
}

func todaySnapshotEmpty(snapshot today.Snapshot) bool {
	return len(snapshot.SourceIDs) == 0 && len(snapshot.ActiveSubjects) == 0 && len(snapshot.RecentChanges) == 0 &&
		len(snapshot.ConfirmedTodos) == 0 && len(snapshot.TodoCandidates) == 0 && len(snapshot.OpenLoops) == 0 &&
		len(snapshot.Questions) == 0 && len(snapshot.TimeSplit) == 0
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
