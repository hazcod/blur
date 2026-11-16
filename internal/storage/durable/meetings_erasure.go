package durable

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/storage"
)

// removeMeetingSourceLocked is a one-to-one bounded reverse edge. Its fence
// survives after all identifying metadata and text have been removed.
func (tx *Tx) removeMeetingSourceLocked(sourceID string) error {
	raw, found, err := tx.readIndexValueLocked(meetingSourceNS, sourceID, 64)
	key := string(raw)
	clear(raw)
	if err != nil || !found {
		return err
	}
	if !validHash(key) {
		return ErrCorrupt
	}
	row, found, err := tx.store.meetingRowReader(tx.writer, key)
	if err != nil {
		return err
	}
	if !found || row.SourceID != sourceID {
		return ErrCorrupt
	}
	h, live, err := tx.store.meetingHeaderReader(tx.writer, row.Record.SessionID)
	if err != nil {
		return err
	}
	if live {
		if h.Meeting.RecordCount == 0 {
			return ErrCorrupt
		}
		h.Meeting.RecordCount--
		switch row.Record.Kind {
		case meetings.TranscriptFinal:
			h.Finals--
			h.TranscriptBytes -= row.Bytes
		case meetings.UserNote:
			h.Notes--
			h.ManualBytes -= row.Bytes
		default:
			return ErrCorrupt
		}
		if h.Finals < 0 || h.Notes < 0 || h.TranscriptBytes < 0 || h.ManualBytes < 0 {
			return ErrCorrupt
		}
		if h.Meeting.RecordCount == 0 {
			if err := tx.removeMeetingHeaderLocked(h); err != nil {
				return err
			}
		} else if err := tx.putIndexValueLocked(meetingHeadNS, h.Meeting.ID, mustJSON(h)); err != nil {
			return err
		}
	}
	if err := tx.putIndexValueLocked(meetingRowFenceNS, key, []byte{1}); err != nil {
		return err
	}
	for _, entry := range [][2]string{{meetingSourceNS, sourceID}, {meetingOrderNS, meetingOrderID(row.Record.SessionID, row.Sequence)}, {meetingRowNS, key}} {
		if err := tx.deleteIndexValueLocked(entry[0], entry[1]); err != nil {
			return err
		}
	}
	return nil
}
func (tx *Tx) removeMeetingHeaderLocked(h meetingHeader) error {
	d, err := tx.store.meetingDirectoryReader(tx.writer)
	if err != nil {
		return err
	}
	if d.Live == 0 {
		return ErrCorrupt
	}
	d.Live--
	if err := tx.putIndexValueLocked(meetingFenceNS, h.Meeting.ID, []byte{1}); err != nil {
		return err
	}
	if err := tx.putIndexValueLocked(meetingMetaNS, "directory", mustJSON(d)); err != nil {
		return err
	}
	if err := tx.deleteIndexValueLocked(meetingListNS, meetingListID(h.Order)); err != nil {
		return err
	}
	return tx.deleteIndexValueLocked(meetingHeadNS, h.Meeting.ID)
}
func (s *Store) DeleteMeeting(ctx context.Context, id string) error {
	return s.deleteMeetingWithHook(ctx, id, nil)
}
func (s *Store) deleteMeetingWithHook(ctx context.Context, id string, afterStep func() error) error {
	_, err := s.deleteMeeting(ctx, id, afterStep, nil)
	return err
}

// Whole-meeting deletion reuses the existing authenticated erasure journal and
// global store gate. The journal enumerates ordinal links one at a time, then
// runs the full existing source/projection cascade for each source. Reopening
// the store resumes that journal before any reads or writes are admitted.
func (s *Store) deleteMeeting(ctx context.Context, id string, afterStep func() error, emptyBefore *time.Time) (bool, error) {
	if ctx == nil || !meetingID(id, 64) {
		return false, meetings.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s == nil || s.state == nil {
		return false, &storage.StoreError{Code: storage.Closed}
	}
	state := s.state
	state.deletionPending.Store(true)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.deletionPending.Store(true)
	if state.closed || state.db == nil {
		return false, &storage.StoreError{Code: storage.Closed}
	}
	journal, found, err := s.readSourceErasureLocked(ctx)
	if err != nil {
		return false, err
	}
	durable := found
	defer func() {
		if !durable {
			state.deletionPending.Store(false)
		}
	}()
	if found && journal.MeetingID != id {
		return false, ErrDeletionPending
	}
	existed := found
	if !found {
		skipped := false
		err = s.updateLocked(ctx, func(tx *Tx) error {
			tx.mu.Lock()
			defer tx.mu.Unlock()
			h, live, err := s.meetingHeaderReader(tx.writer, id)
			if err != nil {
				return err
			}
			existed = live
			if emptyBefore != nil && (!live || h.Meeting.RecordCount != 0 || !h.Meeting.StartedAt.Before(*emptyBefore)) {
				skipped = true
				return nil
			}
			if !live {
				// Fencing an unknown ID also makes a replayed create fail closed.
				return tx.putIndexValueLocked(meetingFenceNS, id, []byte{1})
			}
			journal = sourceErasure{MeetingID: id, MeetingLast: h.LastSequence, MeetingNext: 1, Phase: 14, SourceID: s.meetingDigest("erase", []byte(id))}
			if err := tx.removeMeetingHeaderLocked(h); err != nil {
				return err
			}
			return tx.putIndexValueLocked(erasureNamespace, erasureJournalID, mustJSON(journal))
		})
		if err != nil {
			return false, err
		}
		if skipped || !existed {
			return false, nil
		}
		durable = true
	}
	_, err = s.runSourceErasureLocked(ctx, &journal, afterStep)
	return existed, err
}
func (tx *Tx) meetingPlanErasureStep(j *sourceErasure) (bool, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if j.MeetingID == "" || j.CaptureScope || j.MeetingNext == 0 {
		return false, ErrCorrupt
	}
	if j.MeetingNext > j.MeetingLast {
		return true, nil
	}
	raw, found, err := tx.readIndexValueLocked(meetingOrderNS, meetingOrderID(j.MeetingID, j.MeetingNext), 64)
	key := string(raw)
	clear(raw)
	if err != nil {
		return false, err
	}
	if !found {
		j.MeetingNext++
		return false, nil
	}
	if !validHash(key) {
		return false, ErrCorrupt
	}
	row, live, err := tx.store.meetingRowReader(tx.writer, key)
	if err != nil {
		return false, err
	}
	if !live || row.Record.SessionID != j.MeetingID || row.Sequence != j.MeetingNext {
		return false, ErrCorrupt
	}
	j.SourceID = row.SourceID
	j.Phase = 0
	return false, nil
}

// DeleteMeetingsBefore removes only empty metadata. Observation retention uses
// each record's exact immutable capture time and has already cascaded its edge;
// a newer final must survive even when the meeting started before the cutoff.
func (s *Store) DeleteMeetingsBefore(ctx context.Context, cutoff time.Time) (int, error) {
	if !validCaptureTime(cutoff) {
		return 0, meetings.ErrInvalid
	}
	return s.deleteMeetingMetadata(ctx, &cutoff)
}
func (s *Store) DeleteAllMeetings(ctx context.Context) (int, error) {
	return s.deleteMeetingMetadata(ctx, nil)
}
func (s *Store) deleteMeetingMetadata(ctx context.Context, cutoff *time.Time) (int, error) {
	count := 0
	var cursor uint64
	for {
		page, err := s.ListMeetings(ctx, meetings.PageRequest{After: cursor})
		if err != nil {
			return count, err
		}
		for _, m := range page.Meetings {
			if cutoff != nil && (m.RecordCount != 0 || !m.StartedAt.Before(*cutoff)) {
				continue
			}
			removed, err := s.deleteMeeting(ctx, m.ID, nil, cutoff)
			if err != nil {
				return count, err
			}
			if removed {
				count++
			}
		}
		if !page.More {
			return count, nil
		}
		cursor = page.Next
	}
}
