package durable

import (
	"context"

	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// MeetingForSource resolves one authenticated live record through the existing
// reverse edge. App IDs and opaque domains alone never grant session membership.
func (s *Store) MeetingForSource(ctx context.Context, sourceID string) (meetings.Meeting, bool, error) {
	if ctx == nil || !validHash(sourceID) {
		return meetings.Meeting{}, false, meetings.ErrInvalid
	}
	var result meetings.Meeting
	found := false
	err := s.view(ctx, func(reader bolt.Reader) error {
		o, live, err := s.getObservationReader(reader, sourceID)
		if err != nil || !live {
			return err
		}
		if o.AppID() != meetingAppID {
			return nil
		}
		raw, linked, err := s.readIndexValueReader(reader, meetingSourceNS, sourceID, 64)
		key := string(raw)
		clear(raw)
		if err != nil || !linked {
			return err
		}
		if !validHash(key) {
			return ErrCorrupt
		}
		if fenced, err := s.meetingFenceReader(reader, meetingRowFenceNS, key); err != nil {
			return err
		} else if fenced {
			return nil
		}
		row, live, err := s.meetingRowReader(reader, key)
		if err != nil {
			return err
		}
		if !live || row.SourceID != sourceID || o.Domain() != key+".meeting.local" || row.Bytes != len(o.Text()) {
			return ErrCorrupt
		}
		if fenced, err := s.meetingFenceReader(reader, meetingFenceNS, row.Record.SessionID); err != nil {
			return err
		} else if fenced {
			return nil
		}
		h, live, err := s.meetingHeaderReader(reader, row.Record.SessionID)
		if err != nil || !live {
			return err
		}
		if row.Sequence > h.LastSequence {
			return ErrCorrupt
		}
		order, linked, err := s.readIndexValueReader(reader, meetingOrderNS, meetingOrderID(h.Meeting.ID, row.Sequence), 64)
		matches := string(order) == key
		clear(order)
		if err != nil {
			return err
		}
		if !linked || !matches {
			return ErrCorrupt
		}
		record := row.Record
		switch record.Kind {
		case meetings.TranscriptFinal:
			record.Event.Text = o.Text()
		case meetings.UserNote:
			record.Note.Text = o.Text()
		default:
			return ErrCorrupt
		}
		if _, _, err := validateMeetingRecord(h, record); err != nil {
			return ErrCorrupt
		}
		result, found = h.Meeting, true
		return nil
	})
	if err != nil {
		return meetings.Meeting{}, false, err
	}
	return result, found, nil
}
