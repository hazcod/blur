package durable

import (
	"context"
	"slices"

	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/tasks"
)

// CleanupReminderIDs authenticates a bounded native snapshot and returns only
// identifiers whose task/source authority is stale or is about to be erased.
// Unlike ordinary reads, this cleanup-only decision may inspect metadata while
// a committed erasure is pending. No plaintext task/source data escapes it.
func (s *Store) CleanupReminderIDs(ctx context.Context, target string, ids []string) ([]string, error) {
	if ctx == nil || !meetingID(target, 64) || len(ids) > reminders.MaxSnapshotIDs {
		return nil, tasks.ErrInvalid
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if _, _, valid := reminders.NotificationTaskID(id); !valid || seen[id] {
			return nil, tasks.ErrInvalid
		}
		seen[id] = true
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.state == nil {
		return nil, &storage.StoreError{Code: storage.Closed}
	}
	state := s.state
	if !state.mu.TryRLock() {
		return nil, ErrDeletionPending
	}
	defer state.mu.RUnlock()
	if state.closed || state.db == nil {
		return nil, &storage.StoreError{Code: storage.Closed}
	}
	var remove []string
	for _, id := range ids {
		// One native ID has at most tasks.MaxSources small source receipts.
		// Separate transactions retain the physical operation/byte budgets even
		// when the native snapshot is full and every task has eight sources.
		var cancel bool
		err := s.captureErasureViewLocked(ctx, func(reader bolt.Reader) error {
			var err error
			cancel, err = s.cleanupReminderReader(reader, target, id)
			return err
		})
		if err != nil {
			return nil, err
		}
		if cancel {
			remove = append(remove, id)
		}
	}
	return remove, nil
}

func (s *Store) cleanupReminderReader(reader bolt.Reader, target, nativeID string) (bool, error) {
	taskID, revision, _ := reminders.NotificationTaskID(nativeID)
	item, live, err := s.taskReader(reader, taskID)
	if err != nil {
		return false, err
	}
	if !live || item.Revision != revision || item.State != tasks.Open || item.Reminder == nil {
		return true, nil
	}
	var journal sourceErasure
	raw, pending, err := s.readIndexValueReader(reader, erasureNamespace, erasureJournalID, 2048)
	defer clear(raw)
	if err != nil {
		return false, err
	}
	if pending && (decodeCanonicalJSON(raw, &journal) != nil || !journal.valid()) {
		return false, ErrCorrupt
	}
	for _, sourceID := range item.Sources() {
		linked, err := s.taskSourceReader(reader, sourceID)
		if err != nil {
			return false, err
		}
		if !slices.Contains(linked, item.ID) {
			return true, nil
		}
		cancel, err := s.cleanupReminderSourceReader(reader, target, sourceID, journal, pending)
		if err != nil || cancel {
			return cancel, err
		}
	}
	return false, nil
}

func (s *Store) cleanupReminderSourceReader(reader bolt.Reader, target, sourceID string, journal sourceErasure, pending bool) (bool, error) {
	if pending && (sourceID == journal.SourceID || sourceID == journal.RequestedID) {
		return true, nil
	}
	at, live, err := s.sourceAdmissionReader(reader, sourceID)
	if err != nil || !live {
		return !live, err
	}
	mapping, part, err := s.captureIndexPartReader(reader, sourceID)
	if err != nil {
		return false, err
	}
	if part {
		if pending && (mapping.EditionID == journal.SourceID || mapping.EditionID == journal.RequestedID) {
			return true, nil
		}
		if pending && journal.CaptureScope {
			edition, summary, live, err := s.captureEditionReader(reader, mapping.EditionID)
			if err != nil || !live {
				return !live, err
			}
			if (journal.Family == "" || summary.Header().FamilyID() == journal.Family) && journal.selects(edition.LastSeen) {
				return true, nil
			}
		}
	} else if covered, err := s.deletionCoversReader(reader, sourceID, at); err != nil || covered {
		return covered, err
	}
	ref, note, err := s.noteDocumentSourceReader(reader, sourceID)
	if err != nil {
		return false, err
	}
	if note {
		if pending && ref.ID == journal.NoteID {
			return true, nil
		}
		current, err := s.isCurrentNoteSourceReader(reader, sourceID)
		if err != nil || !current {
			return !current, err
		}
	}
	raw, linked, err := s.readIndexValueReader(reader, meetingSourceNS, sourceID, 64)
	key := string(raw)
	clear(raw)
	if err != nil || !linked {
		return false, err
	}
	if !validHash(key) {
		return false, ErrCorrupt
	}
	row, live, err := s.meetingRowReader(reader, key)
	if err != nil {
		return false, err
	}
	if !live || row.SourceID != sourceID {
		return true, nil
	}
	if row.Record.SessionID == target || pending && row.Record.SessionID == journal.MeetingID {
		return true, nil
	}
	for _, fence := range [][2]string{{meetingRowFenceNS, key}, {meetingFenceNS, row.Record.SessionID}} {
		fenced, err := s.meetingFenceReader(reader, fence[0], fence[1])
		if err != nil || fenced {
			return fenced, err
		}
	}
	head, live, err := s.meetingHeaderReader(reader, row.Record.SessionID)
	if err != nil || !live {
		return !live, err
	}
	if row.Sequence > head.LastSequence {
		return true, nil
	}
	order, linked, err := s.readIndexValueReader(reader, meetingOrderNS, meetingOrderID(row.Record.SessionID, row.Sequence), 64)
	matches := string(order) == key
	clear(order)
	return !linked || !matches, err
}
