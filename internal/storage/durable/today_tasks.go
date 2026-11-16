package durable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/tasks"
	"github.com/hazcod/blur/internal/today"
)

var _ tasks.TodayRepository = (*Store)(nil)
var _ tasks.TodayDismissRepository = (*Store)(nil)

// DismissTodayTask records only the exact suggestion the user reviewed. The
// encrypted receipt is retained across projection replacement and never
// creates a task, notification, or change to the original evidence.
func (s *Store) DismissTodayTask(ctx context.Context, accepted tasks.TodayCandidate) error {
	if s == nil || s.state == nil {
		return ErrUnavailable
	}
	if ctx == nil {
		return tasks.ErrInvalid
	}
	if err := accepted.Validate(); err != nil {
		return err
	}
	accepted.SourceIDs = slices.Clone(accepted.SourceIDs)
	s.state.noteMu.Lock()
	defer s.state.noteMu.Unlock()
	return s.taskAtomicUpdate(ctx, func(tx *Tx) error {
		encoded, found, err := tx.readIndexValue(todayNamespace, accepted.Date, today.MaxSnapshotBytes)
		if err != nil {
			return err
		}
		if !found {
			return tasks.ErrConflict
		}
		defer clear(encoded)
		var snapshot today.Snapshot
		if json.Unmarshal(encoded, &snapshot) != nil || snapshot.Validate() != nil || snapshot.Date != accepted.Date {
			return ErrCorrupt
		}
		matches := func(todo today.Todo) bool {
			return todo.ID == accepted.ID && todo.Text == accepted.Text && slices.Equal(todo.SourceIDs, accepted.SourceIDs)
		}
		for _, todo := range snapshot.DismissedTodos {
			if matches(todo) {
				return nil
			}
		}
		for i, todo := range snapshot.TodoCandidates {
			if !matches(todo) {
				continue
			}
			for _, id := range todo.SourceIDs {
				current, err := s.isCurrentNoteSourceReader(tx.writer, id)
				if err != nil {
					return err
				}
				if !current {
					return tasks.ErrSourceGone
				}
			}
			if len(snapshot.DismissedTodos) >= today.MaxTodos {
				return tasks.ErrCapacity
			}
			snapshot.DismissedTodos = append(snapshot.DismissedTodos, todo)
			snapshot.TodoCandidates = slices.Delete(snapshot.TodoCandidates, i, i+1)
			return tx.putTodaySnapshot(snapshot)
		}
		return tasks.ErrConflict
	})
}

func findTodayTodo(snapshot today.Snapshot, id string) (today.Todo, bool) {
	for _, set := range [][]today.Todo{snapshot.ConfirmedTodos, snapshot.TodoCandidates} {
		for _, todo := range set {
			if todo.ID == id {
				return todo, true
			}
		}
	}
	return today.Todo{}, false
}

// ConfirmTodayTask commits the encrypted task, every source reverse link and
// its Today receipt together. Proposed dates never become notification intent.
func (s *Store) ConfirmTodayTask(ctx context.Context, accepted tasks.TodayCandidate, at time.Time) (tasks.Item, error) {
	if s == nil || s.state == nil {
		return tasks.Item{}, ErrUnavailable
	}
	if ctx == nil || !tasks.ValidTime(at) {
		return tasks.Item{}, tasks.ErrInvalid
	}
	if err := accepted.Validate(); err != nil {
		return tasks.Item{}, err
	}
	accepted.SourceIDs = append([]string(nil), accepted.SourceIDs...)
	s.state.noteMu.Lock()
	defer s.state.noteMu.Unlock()
	var result tasks.Item
	err := s.taskAtomicUpdate(ctx, func(tx *Tx) error {
		encoded, found, err := tx.readIndexValue(todayNamespace, accepted.Date, today.MaxSnapshotBytes)
		if err != nil {
			return err
		}
		if !found {
			return tasks.ErrConflict
		}
		defer clear(encoded)
		var snapshot today.Snapshot
		if json.Unmarshal(encoded, &snapshot) != nil || snapshot.Validate() != nil || snapshot.Date != accepted.Date {
			return ErrCorrupt
		}
		candidate, found := findTodayTodo(snapshot, accepted.ID)
		if !found || candidate.Text != accepted.Text || !slices.Equal(candidate.SourceIDs, accepted.SourceIDs) {
			return tasks.ErrConflict
		}
		identity := sha256.Sum256([]byte("blur-today-task-v1\x00" + accepted.Date + "\x00" + accepted.ID))
		id := hex.EncodeToString(identity[:16])
		body, _ := json.Marshal(accepted)
		hash := sha256.Sum256(body)
		clear(body)
		requestHash := hex.EncodeToString(hash[:])
		if candidate.TaskID != "" && candidate.TaskID != id {
			return tasks.ErrConflict
		}
		tx.mu.Lock()
		item, exists, err := s.taskReader(tx.writer, id)
		if err == nil && exists && (item.RequestHash != requestHash || !slices.Equal(item.Sources(), accepted.SourceIDs)) {
			err = tasks.ErrConflict
		}
		if err == nil && !exists && candidate.TaskID != "" {
			err = tasks.ErrConflict
		}
		if err == nil && !exists {
			item = tasks.Item{ID: id, RequestHash: requestHash, Revision: 1, Title: accepted.Text, State: tasks.Open, CreatedAt: at.UTC(), UpdatedAt: at.UTC(), SourceID: accepted.SourceIDs[0]}
			if len(accepted.SourceIDs) > 1 {
				item.SourceIDs = append([]string(nil), accepted.SourceIDs...)
			}
			tasks.RecordTransition(&item, "")
			if item.Validate() != nil {
				err = tasks.ErrInvalid
			} else {
				err = tx.putTaskRevisionLocked(item, 0)
			}
		}
		tx.mu.Unlock()
		if err != nil {
			return err
		}
		candidate.Confirmed, candidate.TaskID = true, id
		pending := snapshot.TodoCandidates[:0]
		for _, todo := range snapshot.TodoCandidates {
			if todo.ID != candidate.ID {
				pending = append(pending, todo)
			}
		}
		clear(snapshot.TodoCandidates[len(pending):])
		snapshot.TodoCandidates = pending
		replaced := false
		for n := range snapshot.ConfirmedTodos {
			if snapshot.ConfirmedTodos[n].ID == candidate.ID {
				snapshot.ConfirmedTodos[n], replaced = candidate, true
			}
		}
		if !replaced {
			if len(snapshot.ConfirmedTodos) >= today.MaxTodos {
				return tasks.ErrCapacity
			}
			snapshot.ConfirmedTodos = append(snapshot.ConfirmedTodos, candidate)
		}
		if err := tx.putTodaySnapshot(snapshot); err != nil {
			return err
		}
		result = tasks.Clone(item)
		return nil
	})
	return result, err
}

// A refresh may have read before the user reviewed a task. Preserve dismissal
// decisions and confirmed task links instead of restoring old suggestions.
// Confirmed task rows are only linked when the new projection includes them.
func retainTodayTaskReceipts(previous, next today.Snapshot) (today.Snapshot, error) {
	var err error
	next, err = retainTodayDismissals(previous, next)
	if err != nil {
		return today.Snapshot{}, err
	}
	linked := map[string]today.Todo{}
	for _, todo := range previous.ConfirmedTodos {
		if todo.TaskID != "" {
			linked[todo.ID] = todo
		}
	}
	if len(linked) == 0 {
		return next, nil
	}
	next = next.Clone()
	confirmed := map[string]bool{}
	for n, todo := range next.ConfirmedTodos {
		if old, ok := linked[todo.ID]; ok && slices.Equal(old.SourceIDs, todo.SourceIDs) {
			if todo.TaskID != "" && todo.TaskID != old.TaskID {
				return today.Snapshot{}, today.ErrInvalid
			}
			next.ConfirmedTodos[n].TaskID = old.TaskID
		}
		confirmed[todo.ID] = true
	}
	pending := next.TodoCandidates[:0]
	for _, todo := range next.TodoCandidates {
		if old, ok := linked[todo.ID]; ok && slices.Equal(old.SourceIDs, todo.SourceIDs) {
			if !confirmed[todo.ID] {
				if len(next.ConfirmedTodos) >= today.MaxTodos {
					return today.Snapshot{}, today.ErrInvalid
				}
				todo.Confirmed, todo.TaskID = true, old.TaskID
				next.ConfirmedTodos = append(next.ConfirmedTodos, todo)
				confirmed[todo.ID] = true
			}
			continue
		}
		pending = append(pending, todo)
	}
	clear(next.TodoCandidates[len(pending):])
	next.TodoCandidates = pending
	return next, next.Validate()
}

func retainTodayDismissals(previous, next today.Snapshot) (today.Snapshot, error) {
	if len(previous.DismissedTodos) == 0 && len(next.DismissedTodos) == 0 {
		return next, nil
	}
	next = next.Clone()
	// Previous decisions remain authoritative even when an intermediate
	// generation has no suggestions. The source reverse index keeps erasure
	// bounded and removes these receipts along with their evidence.
	for _, old := range previous.DismissedTodos {
		found := false
		for _, item := range next.DismissedTodos {
			if item.ID == old.ID {
				if item.Text != old.Text || !slices.Equal(item.SourceIDs, old.SourceIDs) {
					return today.Snapshot{}, today.ErrInvalid
				}
				found = true
			}
		}
		if !found {
			next.DismissedTodos = append(next.DismissedTodos, old)
		}
	}
	for _, dismissed := range next.DismissedTodos {
		for _, id := range dismissed.SourceIDs {
			if !slices.Contains(next.SourceIDs, id) {
				next.SourceIDs = append(next.SourceIDs, id)
			}
		}
		next.TodoCandidates = slices.DeleteFunc(next.TodoCandidates, func(item today.Todo) bool {
			return item.ID == dismissed.ID && slices.Equal(item.SourceIDs, dismissed.SourceIDs)
		})
	}
	sort.Strings(next.SourceIDs)
	return next, next.Validate()
}
