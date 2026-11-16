package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"

	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/tasks"
)

const (
	taskNS             = "tasks-v1"
	taskSourceNS       = "task-source-v1"
	taskOrderKind byte = 0x07
)

var _ tasks.Repository = (*Store)(nil)

func (s *Store) taskUpdate(ctx context.Context, fn func(*Tx) error) error {
	return s.taskAtomicUpdate(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return fn(tx)
	})
}

func (s *Store) taskAtomicUpdate(ctx context.Context, fn func(*Tx) error) error {
	if ctx == nil {
		return tasks.ErrInvalid
	}
	if !s.state.taskMu.TryRLock() {
		return ErrUnavailable
	}
	defer s.state.taskMu.RUnlock()
	var outcome error
	err := s.Update(ctx, func(tx *Tx) error {
		err := fn(tx)
		for _, expected := range []error{tasks.ErrInvalid, tasks.ErrConflict, tasks.ErrSourceGone, tasks.ErrCapacity} {
			if errors.Is(err, expected) {
				outcome = expected
				return &storage.StoreError{Code: storage.Invalid}
			}
		}
		return err
	})
	if err != nil && outcome != nil {
		return outcome
	}
	return err
}

func (s *Store) taskReader(reader bolt.Reader, id string) (tasks.Item, bool, error) {
	encoded, found, err := s.readIndexValueReader(reader, taskNS, id, tasks.MaxItemBytes)
	if err != nil || !found {
		return tasks.Item{}, found, err
	}
	defer clear(encoded)
	var item tasks.Item
	if json.Unmarshal(encoded, &item) != nil || item.ID != id || item.Validate() != nil {
		return tasks.Item{}, false, ErrCorrupt
	}
	return item, true, nil
}
func (s *Store) taskSourceReader(reader bolt.Reader, sourceID string) ([]string, error) {
	encoded, found, err := s.readIndexValueReader(reader, taskSourceNS, sourceID, 4096)
	if err != nil || !found {
		return nil, err
	}
	defer clear(encoded)
	var ids []string
	if json.Unmarshal(encoded, &ids) != nil || len(ids) > tasks.MaxSourceTasks {
		return nil, ErrCorrupt
	}
	for i, id := range ids {
		if !tasks.ValidID(id) || i > 0 && ids[i-1] >= id {
			return nil, ErrCorrupt
		}
	}
	return ids, nil
}
func (s *Store) GetTask(ctx context.Context, id string) (tasks.Item, bool, error) {
	if ctx == nil || !tasks.ValidID(id) {
		return tasks.Item{}, false, tasks.ErrInvalid
	}
	var item tasks.Item
	var found bool
	err := s.view(ctx, func(reader bolt.Reader) error { var err error; item, found, err = s.taskReader(reader, id); return err })
	return item, found, err
}
func (tx *Tx) putTaskLocked(item tasks.Item) error {
	if item.Validate() != nil {
		return tasks.ErrInvalid
	}
	encoded := mustJSON(item)
	defer clear(encoded)
	if len(encoded) > tasks.MaxItemBytes {
		return tasks.ErrCapacity
	}
	if err := tx.putIndexValueLocked(taskNS, item.ID, encoded); err != nil {
		return err
	}
	key := tx.store.logicalKey(groupOrder, taskOrderKind, taskNS, item.ID)
	defer clear(key)
	envelope, err := tx.store.seal(key, taskNS, []byte(item.ID))
	if err != nil {
		return err
	}
	defer clear(envelope)
	return tx.writeEntryLocked(key, envelope, true)
}
func (s *Store) PutTask(ctx context.Context, item tasks.Item, expected uint64) error {
	if len(item.History) > tasks.MaxHistory || item.HistoryOmitted > 1<<48 {
		return tasks.ErrInvalid
	}
	base := item
	base.History, base.HistoryOmitted = nil, 0
	if ctx == nil || base.Validate() != nil || expected >= 1<<48 || item.Revision != expected+1 {
		return tasks.ErrInvalid
	}
	item = tasks.Clone(item)
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	item.Delivery.At = item.Delivery.At.UTC()
	item.Delivery.NextAt = item.Delivery.NextAt.UTC()
	if item.Reminder != nil {
		item.Reminder.DueAt = item.Reminder.DueAt.UTC()
	}
	return s.taskUpdate(ctx, func(tx *Tx) error { return tx.putTaskRevisionLocked(item, expected) })
}

func (tx *Tx) putTaskRevisionLocked(item tasks.Item, expected uint64) error {
	s := tx.store
	old, found, err := s.taskReader(tx.writer, item.ID)
	if err != nil {
		return err
	}
	if found && (old.Revision != expected || !old.CreatedAt.Equal(item.CreatedAt) || old.SourceID != item.SourceID || !slices.Equal(old.SourceIDs, item.SourceIDs) || old.UserAuthored != item.UserAuthored || old.RequestHash != item.RequestHash) || !found && expected != 0 {
		return tasks.ErrConflict
	}
	if found && item.UpdatedAt.Before(old.UpdatedAt) {
		return tasks.ErrInvalid
	}
	// Transition history is owned by the repository, never by editable UI
	// fields or model proposals. Legacy items without events remain honest
	// about their unknown history until an actual state transition occurs.
	if found {
		item.History = append([]tasks.Transition(nil), old.History...)
		item.HistoryOmitted = old.HistoryOmitted
		tasks.RecordTransition(&item, old.State)
	} else {
		item.History = nil
		item.HistoryOmitted = 0
		tasks.RecordTransition(&item, "")
	}
	for _, sourceID := range item.Sources() {
		_, _, live, err := s.sourceEvidenceReader(tx.writer, sourceID)
		if err != nil {
			return err
		}
		if !live {
			return tasks.ErrSourceGone
		}
		if !found {
			current, err := s.isCurrentNoteSourceReader(tx.writer, sourceID)
			if err != nil {
				return err
			}
			if !current {
				return tasks.ErrSourceGone
			}
			ids, err := s.taskSourceReader(tx.writer, sourceID)
			if err != nil {
				return err
			}
			if slices.Contains(ids, item.ID) {
				return ErrCorrupt
			}
			if len(ids) >= tasks.MaxSourceTasks {
				return tasks.ErrCapacity
			}
			ids = append(ids, item.ID)
			sort.Strings(ids)
			if err := tx.putIndexValueLocked(taskSourceNS, sourceID, mustJSON(ids)); err != nil {
				return err
			}
		}
	}
	return tx.putTaskLocked(item)
}
func (s *Store) SetTaskDelivery(ctx context.Context, id string, revision uint64, delivery tasks.Delivery) error {
	if ctx == nil || !tasks.ValidID(id) || revision == 0 || delivery.State == "" || delivery.Validate() != nil {
		return tasks.ErrInvalid
	}
	delivery.At = delivery.At.UTC()
	delivery.NextAt = delivery.NextAt.UTC()
	return s.taskUpdate(ctx, func(tx *Tx) error {
		item, found, err := s.taskReader(tx.writer, id)
		if err != nil {
			return err
		}
		if !found || item.Revision != revision || item.State != tasks.Open || item.Reminder == nil {
			return tasks.ErrConflict
		}
		item.Delivery = delivery
		return tx.putTaskLocked(item)
	})
}
func (s *Store) taskOrderID(key, value []byte) (string, error) {
	if len(value) > 256 {
		return "", ErrCorrupt
	}
	plain, err := s.open(key, taskNS, value)
	if err != nil {
		return "", ErrCorrupt
	}
	defer clear(plain)
	id := string(plain)
	if !tasks.ValidID(id) {
		return "", ErrCorrupt
	}
	expected := s.logicalKey(groupOrder, taskOrderKind, taskNS, id)
	defer clear(expected)
	if !bytes.Equal(key, expected) {
		return "", ErrCorrupt
	}
	return id, nil
}
func (s *Store) ListTasks(ctx context.Context, request tasks.PageRequest) (tasks.Page, error) {
	if ctx == nil || request.Limit < 1 || request.Limit > tasks.MaxPageSize || request.After != "" && !tasks.ValidID(request.After) || request.State != "" && request.State != tasks.Open && request.State != tasks.Completed && request.State != tasks.Cancelled {
		return tasks.Page{}, tasks.ErrInvalid
	}
	var after []byte
	if request.After != "" {
		var err error
		after, err = s.cursorKey(groupOrder, taskOrderKind, taskNS, request.After)
		if err != nil {
			return tasks.Page{}, err
		}
		defer clear(after)
	}
	page := tasks.Page{Items: []tasks.Item{}}
	err := s.view(ctx, func(reader bolt.Reader) error {
		entries, more, err := reader.Scan([]byte{groupOrder, taskOrderKind}, after, request.Limit, 32<<10)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		defer clearBoltEntries(entries)
		page.HasMore = more
		for _, entry := range entries {
			id, err := s.taskOrderID(entry.Key, entry.Value)
			if err != nil {
				return err
			}
			item, found, err := s.taskReader(reader, id)
			if err != nil {
				return err
			}
			if !found {
				return ErrCorrupt
			}
			if request.State == "" || item.State == request.State {
				page.Items = append(page.Items, item)
			}
			page.Next = id
		}
		if !more {
			page.Next = ""
		}
		return nil
	})
	if err != nil {
		return tasks.Page{}, err
	}
	return page, nil
}
func (s *Store) TasksForSources(ctx context.Context, sources []string) ([]tasks.Item, error) {
	if ctx == nil || len(sources) > 8 {
		return nil, tasks.ErrInvalid
	}
	for _, id := range sources {
		if !tasks.ValidSourceID(id) {
			return nil, tasks.ErrInvalid
		}
	}
	items := []tasks.Item{}
	seen := map[string]bool{}
	err := s.view(ctx, func(reader bolt.Reader) error {
		for _, sourceID := range sources {
			ids, err := s.taskSourceReader(reader, sourceID)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if seen[id] {
					continue
				}
				seen[id] = true
				item, found, err := s.taskReader(reader, id)
				if err != nil {
					return err
				}
				if !found || !item.References(sourceID) {
					return ErrCorrupt
				}
				items = append(items, item)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}
func (tx *Tx) deleteTaskLocked(item tasks.Item) error {
	for _, sourceID := range item.Sources() {
		ids, err := tx.store.taskSourceReader(tx.writer, sourceID)
		if err != nil {
			return err
		}
		if !slices.Contains(ids, item.ID) {
			return ErrCorrupt
		}
		ids = removeString(ids, item.ID)
		if len(ids) == 0 {
			if err := tx.deleteInferredIndex(taskSourceNS, sourceID); err != nil {
				return err
			}
		} else if err := tx.putIndexValueLocked(taskSourceNS, sourceID, mustJSON(ids)); err != nil {
			return err
		}
	}
	return tx.deleteTaskRecordLocked(item)
}

func (tx *Tx) deleteTaskRecordLocked(item tasks.Item) error {
	if err := tx.deleteInferredIndex(taskNS, item.ID); err != nil {
		return err
	}
	key := tx.store.logicalKey(groupOrder, taskOrderKind, taskNS, item.ID)
	defer clear(key)
	return tx.deleteEntryLocked(key)
}
func (s *Store) DeleteTask(ctx context.Context, id string, revision uint64) error {
	if ctx == nil || !tasks.ValidID(id) || revision == 0 {
		return tasks.ErrInvalid
	}
	return s.taskUpdate(ctx, func(tx *Tx) error {
		item, found, err := s.taskReader(tx.writer, id)
		if err != nil {
			return err
		}
		if !found || item.Revision != revision {
			return tasks.ErrConflict
		}
		return tx.deleteTaskLocked(item)
	})
}
func (s *Store) DeleteAllTasks(ctx context.Context) error {
	if ctx == nil {
		return tasks.ErrInvalid
	}
	s.state.taskMu.Lock()
	defer s.state.taskMu.Unlock()
	for {
		page, err := s.ListTasks(ctx, tasks.PageRequest{Limit: tasks.MaxPageSize})
		if err != nil {
			return err
		}
		if len(page.Items) == 0 {
			return nil
		}
		for _, item := range page.Items {
			if err := s.Update(ctx, func(tx *Tx) error { tx.mu.Lock(); defer tx.mu.Unlock(); return tx.deleteTaskLocked(item) }); err != nil {
				return err
			}
		}
	}
}
func (tx *Tx) removeTasksSource(sourceID string) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	ids, err := tx.store.taskSourceReader(tx.writer, sourceID)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	// Many tasks can share the same contextual sources. Rewrite each reverse
	// directory once, rather than repeatedly decrypting and writing it for
	// every removed task (which can exhaust the physical transaction budget).
	otherSources := map[string]map[string]bool{}
	for _, id := range ids {
		item, found, err := tx.store.taskReader(tx.writer, id)
		if err != nil {
			return err
		}
		if !found || !item.References(sourceID) {
			return ErrCorrupt
		}
		for _, other := range item.Sources() {
			if other == sourceID {
				continue
			}
			if otherSources[other] == nil {
				otherSources[other] = map[string]bool{}
			}
			otherSources[other][item.ID] = true
		}
		if err := tx.deleteTaskRecordLocked(item); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(otherSources))
	for other := range otherSources {
		keys = append(keys, other)
	}
	sort.Strings(keys)
	for _, other := range keys {
		linked, err := tx.store.taskSourceReader(tx.writer, other)
		if err != nil {
			return err
		}
		removed, count := otherSources[other], 0
		remaining := slices.DeleteFunc(linked, func(id string) bool {
			if removed[id] {
				count++
				return true
			}
			return false
		})
		if count != len(removed) {
			return ErrCorrupt
		}
		if len(remaining) == 0 {
			if err := tx.deleteInferredIndex(taskSourceNS, other); err != nil {
				return err
			}
		} else if err := tx.putIndexValueLocked(taskSourceNS, other, mustJSON(remaining)); err != nil {
			return err
		}
	}
	return tx.deleteInferredIndex(taskSourceNS, sourceID)
}
