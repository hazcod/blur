package durable

import (
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/today"
)

// Confirmed tasks retain their original evidence. Suggestions and recent-note
// previews describe only current document revisions, even in older snapshots.
func (s *Store) filterNoteDocumentTodayReader(r bolt.Reader, snapshot *today.Snapshot) error {
	current := func(ids []string) (bool, error) {
		if len(ids) == 0 {
			return true, nil
		}
		for _, id := range ids {
			live, err := s.isCurrentNoteSourceReader(r, id)
			if err != nil {
				return false, err
			}
			if live {
				return true, nil
			}
		}
		return false, nil
	}
	todos := snapshot.TodoCandidates[:0]
	for _, todo := range snapshot.TodoCandidates {
		live, err := current(todo.SourceIDs)
		if err != nil {
			return err
		}
		if live {
			todos = append(todos, todo)
		}
	}
	snapshot.TodoCandidates = todos
	changes := snapshot.RecentChanges[:0]
	for _, change := range snapshot.RecentChanges {
		live, err := current(change.SourceIDs)
		if err != nil {
			return err
		}
		if live {
			changes = append(changes, change)
		}
	}
	snapshot.RecentChanges = changes
	questions := snapshot.Questions[:0]
	for _, question := range snapshot.Questions {
		live, err := current(question.SourceIDs)
		if err != nil {
			return err
		}
		if live || question.Resolved {
			questions = append(questions, question)
		}
	}
	snapshot.Questions = questions
	return nil
}
