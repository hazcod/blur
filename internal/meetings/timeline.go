package meetings

import "time"

// TranscriptOrigin is independent of when the meeting/agenda was created.
// Zero RecordingStartedAt keeps the original origin of legacy transcripts.
func (m Meeting) TranscriptOrigin() time.Time {
	if !m.RecordingStartedAt.IsZero() {
		return m.RecordingStartedAt
	}
	return m.StartedAt
}
func validMeetingInstant(at time.Time) bool {
	return !at.IsZero() && time.Unix(0, at.UTC().UnixNano()).UTC().Equal(at.UTC())
}
func validManualNoteTime(note ManualNoteInput) bool {
	return note.At >= 0 && (note.RecordedAt.IsZero() || note.At == 0 && validMeetingInstant(note.RecordedAt))
}

// RecordTimes resolves immutable evidence provenance. Transcript offsets belong
// to the recording origin and retain the 8h audio bound. Explicit manual-note
// times are independent wall times; legacy At remains relative to creation.
// No source text, source timestamp, note offset or metadata is rewritten.
func RecordTimes(m Meeting, r Record) (start, end time.Time, err error) {
	if !validID(m.ID, 64) || !validMeetingInstant(m.StartedAt) || r.SessionID != m.ID {
		return time.Time{}, time.Time{}, ErrInvalid
	}
	switch r.Kind {
	case TranscriptFinal:
		e := r.Event
		if e.Kind != Final || e.SessionID != m.ID || e.Start < 0 || e.End < e.Start || e.End > DefaultLimits().MaxDuration {
			return time.Time{}, time.Time{}, ErrInvalid
		}
		origin := m.TranscriptOrigin()
		if !validMeetingInstant(origin) || origin.Before(m.StartedAt) {
			return time.Time{}, time.Time{}, ErrInvalid
		}
		start, end = origin.Add(e.Start), origin.Add(e.End)
		if !validMeetingInstant(start) || !validMeetingInstant(end) || start.Before(origin) || end.Before(start) {
			return time.Time{}, time.Time{}, ErrInvalid
		}
	case UserNote:
		if !validManualNoteTime(r.Note) {
			return time.Time{}, time.Time{}, ErrInvalid
		}
		start = r.Note.RecordedAt
		if start.IsZero() {
			start = m.StartedAt.Add(r.Note.At)
		}
		if !validMeetingInstant(start) {
			return time.Time{}, time.Time{}, ErrInvalid
		}
	default:
		return time.Time{}, time.Time{}, ErrInvalid
	}
	return start, end, nil
}

// Ignore monotonic clock bookkeeping, which is absent from canonical JSON, but
// keep timezone serialization differences strict like the durable payload HMAC.
func sameManualNote(a, b ManualNoteInput) bool {
	if a.ID != b.ID || a.At != b.At || a.Text != b.Text {
		return false
	}
	if a.RecordedAt.IsZero() && b.RecordedAt.IsZero() {
		return true
	}
	return a.RecordedAt.Format(time.RFC3339Nano) == b.RecordedAt.Format(time.RFC3339Nano)
}
