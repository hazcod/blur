package durable

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const (
	meetingHeadNS        = "meeting-head-v1"
	meetingRowNS         = "meeting-row-v1"
	meetingOrderNS       = "meeting-order-v1"
	meetingSourceNS      = "meeting-source-v1"
	meetingFenceNS       = "meeting-fence-v1"
	meetingRowFenceNS    = "meeting-row-fence-v1"
	meetingListNS        = "meeting-list-v1"
	meetingMetaNS        = "meeting-meta-v1"
	meetingAppID         = "sh.blur.meetings"
	meetingScanRows      = 256
	meetingMetadataBytes = 4096
)

type meetingHeader struct {
	Meeting                      meetings.Meeting
	Order, LastSequence          uint64
	Finals, Notes                int
	TranscriptBytes, ManualBytes int
}
type meetingDirectory struct{ Last, Live uint64 }
type meetingRecord struct {
	Sequence         uint64
	Record           meetings.Record
	SourceID, Digest string
	Bytes            int
}

var _ meetings.Repository = (*Store)(nil)
var _ meetings.RecordLookup = (*Store)(nil)
var _ meetings.AgendaTailReader = (*Store)(nil)

func (s *Store) Capabilities() meetings.SinkCapabilities {
	if s == nil || s.state == nil {
		return meetings.SinkCapabilities{}
	}
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	if s.state.closed || s.state.db == nil || s.state.codec == nil || s.state.deletionPending.Load() {
		return meetings.SinkCapabilities{}
	}
	return meetings.SinkCapabilities{Encryption: meetings.AuthenticatedEncryption, AtomicIdempotent: true}
}
func meetingID(id string, max int) bool {
	if len(id) == 0 || len(id) > max || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}
func meetingText(text string) bool {
	return utf8.ValidString(text) && strings.TrimSpace(text) != "" && !strings.ContainsRune(text, 0)
}

// Called only under a store read/write lock. No unhashed key or raw payload is
// persisted by these digests, including a secret redacted from the source.
func (s *Store) meetingDigest(label string, payload []byte) string {
	h := hmac.New(sha256.New, s.state.idKey)
	_, _ = h.Write([]byte("meeting-v1\x00" + label + "\x00"))
	_, _ = h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}
func meetingOrderID(id string, seq uint64) string { return fmt.Sprintf("%s:%012x", id, seq) }
func meetingListID(order uint64) string           { return fmt.Sprintf("%012x", order) }
func (s *Store) meetingUpdate(ctx context.Context, fn func(*Tx) error) error {
	if ctx == nil {
		return meetings.ErrInvalid
	}
	var outcome error
	err := s.Update(ctx, func(tx *Tx) error {
		err := fn(tx)
		for _, expected := range []error{meetings.ErrInvalid, meetings.ErrNotFound, meetings.ErrDeleted, meetings.ErrConflict, meetings.ErrState, meetings.ErrLimit} {
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
	if errors.Is(err, ErrSourceDeleted) {
		return meetings.ErrDeleted
	}
	return err
}
func (s *Store) meetingHeaderReader(r bolt.Reader, id string) (meetingHeader, bool, error) {
	var h meetingHeader
	raw, found, err := s.readIndexValueReader(r, meetingHeadNS, id, meetingMetadataBytes)
	defer clear(raw)
	if err != nil || !found {
		return h, found, err
	}
	maxInt := int(^uint(0) >> 1)
	if decodeCanonicalJSON(raw, &h) != nil || h.Meeting.ID != id || h.Meeting.Validate() != nil || !validCaptureTime(h.Meeting.StartedAt) || !h.Meeting.EndedAt.IsZero() && !validCaptureTime(h.Meeting.EndedAt) || h.Order == 0 || h.Order >= 1<<48 || h.LastSequence >= 1<<48 || h.Finals < 0 || h.Notes < 0 || h.Finals > maxInt-h.Notes || h.Meeting.RecordCount != uint64(h.Finals+h.Notes) || h.Meeting.RecordCount > h.LastSequence || h.TranscriptBytes < 0 || h.ManualBytes < 0 {
		return meetingHeader{}, false, ErrCorrupt
	}
	return h, true, nil
}
func (s *Store) meetingDirectoryReader(r bolt.Reader) (meetingDirectory, error) {
	raw, found, err := s.readIndexValueReader(r, meetingMetaNS, "directory", 128)
	defer clear(raw)
	var d meetingDirectory
	if err != nil || !found {
		return d, err
	}
	if decodeCanonicalJSON(raw, &d) != nil || d.Last >= 1<<48 || d.Live > d.Last {
		return d, ErrCorrupt
	}
	return d, nil
}
func (s *Store) meetingFenceReader(r bolt.Reader, ns, id string) (bool, error) {
	raw, found, err := s.readIndexValueReader(r, ns, id, 1)
	defer clear(raw)
	if err == nil && found && (len(raw) != 1 || raw[0] != 1) {
		err = ErrCorrupt
	}
	return found, err
}
func (s *Store) meetingRowReader(r bolt.Reader, key string) (meetingRecord, bool, error) {
	raw, found, err := s.readIndexValueReader(r, meetingRowNS, key, meetingMetadataBytes)
	defer clear(raw)
	var row meetingRecord
	if err != nil || !found {
		return row, found, err
	}
	if decodeCanonicalJSON(raw, &row) != nil || row.Sequence == 0 || row.Sequence >= 1<<48 || !validHash(row.SourceID) || !validHash(row.Digest) || row.Bytes < 1 || row.Bytes > meetings.DefaultLimits().MaxNoteBytes || row.Record.Event.Text != "" || row.Record.Note.Text != "" || s.meetingDigest("key", []byte(row.Record.Key)) != key {
		return meetingRecord{}, false, ErrCorrupt
	}
	return row, true, nil
}
func (s *Store) PutMeeting(ctx context.Context, m meetings.Meeting, expected uint64) error {
	if m.Validate() != nil || !validCaptureTime(m.StartedAt) || !m.EndedAt.IsZero() && !validCaptureTime(m.EndedAt) || expected >= 1<<48-1 || m.Revision != expected+1 {
		return meetings.ErrInvalid
	}
	title, err := capture.SanitizeObservation(meetingAppID, "metadata.meeting.local", m.Title, meetings.MaxMeetingTitleBytes)
	if err != nil || title.Truncated() {
		return meetings.ErrInvalid
	}
	m.Title = title.Text()
	m.Tracks = append([]string(nil), m.Tracks...)
	return s.meetingUpdate(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		if fenced, err := s.meetingFenceReader(tx.writer, meetingFenceNS, m.ID); err != nil {
			return err
		} else if fenced {
			return meetings.ErrDeleted
		}
		h, found, err := s.meetingHeaderReader(tx.writer, m.ID)
		if err != nil {
			return err
		}
		if found {
			if h.Meeting.Revision != expected {
				return meetings.ErrConflict
			}
			if !m.StartedAt.Equal(h.Meeting.StartedAt) || !reflect.DeepEqual(m.Tracks, h.Meeting.Tracks) {
				return meetings.ErrInvalid
			}
			// A Ready agenda may acquire its first recording clock without moving
			// creation-relative notes. Once fixed (or legacy finals exist), never rebase.
			if !h.Meeting.RecordingStartedAt.IsZero() {
				if !m.RecordingStartedAt.Equal(h.Meeting.RecordingStartedAt) {
					return meetings.ErrInvalid
				}
			} else if !m.RecordingStartedAt.IsZero() && (h.Meeting.State != meetings.Ready || h.Finals != 0 || m.State != meetings.Ready && m.State != meetings.Running) {
				return meetings.ErrInvalid
			}
			m.RecordCount = h.Meeting.RecordCount
			h.Meeting = m
		} else {
			if expected != 0 {
				return meetings.ErrNotFound
			}
			if deleted, err := s.deletionCoversReader(tx.writer, s.meetingDigest("meeting", []byte(m.ID)), m.StartedAt); err != nil {
				return err
			} else if deleted {
				return meetings.ErrDeleted
			}
			d, err := s.meetingDirectoryReader(tx.writer)
			if err != nil {
				return err
			}
			if d.Last >= 1<<48-1 {
				return meetings.ErrLimit
			}
			d.Last++
			d.Live++
			m.RecordCount = 0
			h = meetingHeader{Meeting: m, Order: d.Last}
			if err := tx.putIndexValueLocked(meetingMetaNS, "directory", mustJSON(d)); err != nil {
				return err
			}
			if err := tx.putIndexValueLocked(meetingListNS, meetingListID(h.Order), []byte(m.ID)); err != nil {
				return err
			}
		}
		return tx.putIndexValueLocked(meetingHeadNS, m.ID, mustJSON(h))
	})
}
func (s *Store) GetMeeting(ctx context.Context, id string) (meetings.Meeting, bool, error) {
	if ctx == nil || !meetingID(id, 64) {
		return meetings.Meeting{}, false, meetings.ErrInvalid
	}
	var h meetingHeader
	var found bool
	err := s.view(ctx, func(r bolt.Reader) error { var err error; h, found, err = s.meetingHeaderReader(r, id); return err })
	return h.Meeting, found, err
}
func (s *Store) ListMeetings(ctx context.Context, p meetings.PageRequest) (meetings.MeetingPage, error) {
	p, err := p.Normalized()
	if err != nil || ctx == nil {
		return meetings.MeetingPage{}, meetings.ErrInvalid
	}
	out := meetings.MeetingPage{Next: p.After}
	err = s.view(ctx, func(r bolt.Reader) error {
		d, err := s.meetingDirectoryReader(r)
		if err != nil {
			return err
		}
		order := d.Last
		if p.After != 0 {
			if p.After > d.Last {
				return meetings.ErrInvalid
			}
			order = p.After - 1
		}
		used := 0
		for scanned := 0; order > 0 && scanned < meetingScanRows && len(out.Meetings) < p.Limit; scanned++ {
			raw, found, err := s.readIndexValueReader(r, meetingListNS, meetingListID(order), 64)
			id := string(raw)
			clear(raw)
			if err != nil {
				return err
			}
			if found {
				h, live, err := s.meetingHeaderReader(r, id)
				if err != nil {
					return err
				}
				if !live || h.Order != order {
					return ErrCorrupt
				}
				size := len(mustJSON(h.Meeting))
				if used+size > p.MaxBytes {
					if len(out.Meetings) == 0 {
						return meetings.ErrLimit
					}
					break
				}
				used += size
				out.Meetings = append(out.Meetings, h.Meeting)
			}
			out.Next = order
			order--
		}
		out.More = order > 0
		return nil
	})
	return out, err
}
func validateMeetingRecord(h meetingHeader, r meetings.Record) (string, time.Time, error) {
	if r.SessionID != h.Meeting.ID {
		return "", time.Time{}, meetings.ErrInvalid
	}
	start, end, timeErr := meetings.RecordTimes(h.Meeting, r)
	if timeErr != nil {
		return "", time.Time{}, timeErr
	}
	l := meetings.DefaultLimits()
	switch r.Kind {
	case meetings.TranscriptFinal:
		e := r.Event
		track := false
		for _, id := range h.Meeting.Tracks {
			track = track || id == e.TrackID
		}
		if e.Kind != meetings.Final || e.SessionID != r.SessionID || !track || !meetingID(e.SegmentID, 64) || e.Revision == 0 || e.Start < 0 || e.End < e.Start || !meetingText(e.Text) || len(e.Text) > l.MaxEventBytes || r.Key != r.SessionID+"/transcript/"+e.TrackID+"/"+e.SegmentID || r.Note != (meetings.ManualNoteInput{}) {
			return "", time.Time{}, meetings.ErrInvalid
		}
		return e.Text, end, nil
	case meetings.UserNote:
		n := r.Note
		if !meetingID(n.ID, 64) || n.At < 0 || !meetingText(n.Text) || len(n.Text) > l.MaxNoteBytes || r.Key != r.SessionID+"/note/"+n.ID || r.Event != (meetings.Event{}) {
			return "", time.Time{}, meetings.ErrInvalid
		}
		return n.Text, start, nil
	default:
		return "", time.Time{}, meetings.ErrInvalid
	}
}
func (s *Store) Append(ctx context.Context, r meetings.Record) (meetings.Receipt, error) {
	if !meetingID(r.SessionID, 64) || len(r.Key) > 256 {
		return meetings.Receipt{}, meetings.ErrInvalid
	}
	var receipt meetings.Receipt
	var observation capture.SanitizedObservation
	err := s.meetingUpdate(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		if fenced, err := s.meetingFenceReader(tx.writer, meetingFenceNS, r.SessionID); err != nil {
			tx.mu.Unlock()
			return err
		} else if fenced {
			tx.mu.Unlock()
			return meetings.ErrDeleted
		}
		h, found, err := s.meetingHeaderReader(tx.writer, r.SessionID)
		if err != nil || !found {
			tx.mu.Unlock()
			if err != nil {
				return err
			}
			return meetings.ErrNotFound
		}
		text, at, err := validateMeetingRecord(h, r)
		if err != nil {
			tx.mu.Unlock()
			return err
		}
		key := s.meetingDigest("key", []byte(r.Key))
		encoded := mustJSON(r)
		digest := s.meetingDigest("payload", encoded)
		clear(encoded)
		if fenced, err := s.meetingFenceReader(tx.writer, meetingRowFenceNS, key); err != nil {
			tx.mu.Unlock()
			return err
		} else if fenced {
			tx.mu.Unlock()
			return meetings.ErrDeleted
		}
		existing, found, err := s.meetingRowReader(tx.writer, key)
		if err != nil {
			tx.mu.Unlock()
			return err
		}
		if found {
			if existing.Digest != digest {
				tx.mu.Unlock()
				return meetings.ErrConflict
			}
			var live bool
			observation, live, err = s.getObservationReader(tx.writer, existing.SourceID)
			tx.mu.Unlock()
			if err != nil {
				return err
			}
			if !live {
				return ErrCorrupt
			}
			receipt.SourceID = existing.SourceID
			return nil
		}
		if r.Kind == meetings.TranscriptFinal && h.Meeting.State != meetings.Running {
			tx.mu.Unlock()
			return meetings.ErrState
		}
		l := meetings.DefaultLimits()
		maxInt := int(^uint(0) >> 1)
		if h.LastSequence >= 1<<48-1 || h.Finals >= maxInt-h.Notes {
			tx.mu.Unlock()
			return meetings.ErrLimit
		}
		// Per-record opaque domains prevent repeated utterances collapsing into one
		// content-addressed source; this also bounds source-to-record erasure work.
		textLimit := l.MaxEventBytes
		if r.Kind == meetings.UserNote {
			textLimit = l.MaxNoteBytes
		}
		observation, err = capture.SanitizeObservation(meetingAppID, key+".meeting.local", text, textLimit)
		if err != nil || observation.Truncated() {
			tx.mu.Unlock()
			return meetings.ErrInvalid
		}
		savedBytes := len(observation.Text())
		if r.Kind == meetings.TranscriptFinal && savedBytes > maxInt-h.TranscriptBytes || r.Kind == meetings.UserNote && savedBytes > maxInt-h.ManualBytes {
			tx.mu.Unlock()
			return meetings.ErrLimit
		}
		h.LastSequence++
		h.Meeting.RecordCount++
		row := meetingRecord{Sequence: h.LastSequence, Record: r, SourceID: observation.ContentID(), Digest: digest, Bytes: savedBytes}
		row.Record.Event.Text = ""
		row.Record.Note.Text = ""
		if r.Kind == meetings.TranscriptFinal {
			h.Finals++
			h.TranscriptBytes += savedBytes
		} else {
			h.Notes++
			h.ManualBytes += savedBytes
		}
		tx.mu.Unlock()
		if err := tx.PutObservationAt(observation, at); err != nil {
			return err
		}
		tx.mu.Lock()
		defer tx.mu.Unlock()
		for _, entry := range []struct {
			ns, id string
			value  []byte
		}{{meetingRowNS, key, mustJSON(row)}, {meetingOrderNS, meetingOrderID(r.SessionID, row.Sequence), []byte(key)}, {meetingSourceNS, row.SourceID, []byte(key)}, {meetingHeadNS, r.SessionID, mustJSON(h)}} {
			if err := tx.putIndexValueLocked(entry.ns, entry.id, entry.value); err != nil {
				return err
			}
		}
		receipt.SourceID = row.SourceID
		return nil
	})
	if err != nil {
		return meetings.Receipt{}, err
	}
	if err := s.indexMeetingObservation(ctx, observation); err != nil {
		if errors.Is(err, ErrSourceDeleted) {
			return meetings.Receipt{}, meetings.ErrDeleted
		}
		return meetings.Receipt{}, err
	}
	return receipt, nil
}

// LookupRecord resolves a stable key against the original payload and current
// erasure fences. It never creates evidence. An existing unfinished exact-index
// receipt is completed before returning the same searchable receipt as Append.
func (s *Store) LookupRecord(ctx context.Context, r meetings.Record) (meetings.Receipt, bool, error) {
	if ctx == nil || !meetingID(r.SessionID, 64) || len(r.Key) > 256 {
		return meetings.Receipt{}, false, meetings.ErrInvalid
	}
	var receipt meetings.Receipt
	var observation capture.SanitizedObservation
	found, pending := false, false
	err := s.view(ctx, func(reader bolt.Reader) error {
		if fenced, err := s.meetingFenceReader(reader, meetingFenceNS, r.SessionID); err != nil {
			return err
		} else if fenced {
			return meetings.ErrDeleted
		}
		h, live, err := s.meetingHeaderReader(reader, r.SessionID)
		if err != nil || !live {
			return err
		}
		if _, _, err := validateMeetingRecord(h, r); err != nil {
			return err
		}
		key := s.meetingDigest("key", []byte(r.Key))
		if fenced, err := s.meetingFenceReader(reader, meetingRowFenceNS, key); err != nil {
			return err
		} else if fenced {
			return meetings.ErrDeleted
		}
		row, live, err := s.meetingRowReader(reader, key)
		if err != nil || !live {
			return err
		}
		encoded := mustJSON(r)
		digest := s.meetingDigest("payload", encoded)
		clear(encoded)
		if row.Digest != digest {
			return meetings.ErrConflict
		}
		metadata := r
		metadata.Event.Text, metadata.Note.Text = "", ""
		storedMetadata, expectedMetadata := mustJSON(row.Record), mustJSON(metadata)
		matches := bytes.Equal(storedMetadata, expectedMetadata)
		clear(storedMetadata)
		clear(expectedMetadata)
		if !matches || row.Sequence > h.LastSequence {
			return ErrCorrupt
		}
		observation, live, err = s.getObservationReader(reader, row.SourceID)
		if err != nil {
			return err
		}
		if !live || observation.AppID() != meetingAppID || observation.Domain() != key+".meeting.local" || len(observation.Text()) != row.Bytes {
			return ErrCorrupt
		}
		for _, link := range [][2]string{
			{meetingSourceNS, row.SourceID},
			{meetingOrderNS, meetingOrderID(r.SessionID, row.Sequence)},
		} {
			raw, linked, err := s.readIndexValueReader(reader, link[0], link[1], 64)
			matches := string(raw) == key
			clear(raw)
			if err != nil {
				return err
			}
			if !linked || !matches {
				return ErrCorrupt
			}
		}
		pendingKey := s.pendingSearchKey(row.SourceID)
		defer clear(pendingKey)
		pending, err = reader.Has(pendingKey)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		receipt.SourceID, found = row.SourceID, true
		return nil
	})
	if err != nil {
		return meetings.Receipt{}, false, err
	}
	if found && pending {
		if err := s.indexMeetingObservation(ctx, observation); err != nil {
			if errors.Is(err, ErrSourceDeleted) {
				return meetings.Receipt{}, false, meetings.ErrDeleted
			}
			return meetings.Receipt{}, false, err
		}
	}
	return receipt, found, nil
}

// ReadyAgendaTail authenticates eligibility and reads a bounded display tail
// from the same snapshot. Historical ordinal holes never expand startup work;
// the last allocated ordinal remains the cursor for future append polling.
func (s *Store) ReadyAgendaTail(ctx context.Context, id string, p meetings.PageRequest) (meetings.AgendaTail, error) {
	p, err := p.Normalized()
	if ctx == nil || err != nil || !meetingID(id, 64) || p.After != 0 {
		return meetings.AgendaTail{}, meetings.ErrInvalid
	}
	limit, maxBytes := min(p.Limit, 20), min(p.MaxBytes, 128<<10)
	var out meetings.AgendaTail
	err = s.view(ctx, func(reader bolt.Reader) error {
		if fenced, err := s.meetingFenceReader(reader, meetingFenceNS, id); err != nil {
			return err
		} else if fenced {
			return meetings.ErrDeleted
		}
		h, found, err := s.meetingHeaderReader(reader, id)
		if err != nil {
			return err
		}
		if !found {
			return meetings.ErrNotFound
		}
		if h.Meeting.State != meetings.Ready || h.Finals != 0 || !h.Meeting.RecordingStartedAt.IsZero() || !h.Meeting.EndedAt.IsZero() {
			return meetings.ErrState
		}
		out.Meeting, out.Cursor = h.Meeting, h.LastSequence
		seq, used := h.LastSequence, 0
		for scanned := 0; seq > 0 && scanned < meetingScanRows && len(out.Records) < limit; scanned++ {
			raw, linked, err := s.readIndexValueReader(reader, meetingOrderNS, meetingOrderID(id, seq), 64)
			key := string(raw)
			clear(raw)
			if err != nil {
				return err
			}
			if linked {
				if !validHash(key) {
					return ErrCorrupt
				}
				row, live, err := s.meetingRowReader(reader, key)
				if err != nil {
					return err
				}
				if !live || row.Sequence != seq || row.Record.SessionID != id || row.Record.Kind != meetings.UserNote {
					return ErrCorrupt
				}
				if fenced, err := s.meetingFenceReader(reader, meetingRowFenceNS, key); err != nil {
					return err
				} else if fenced {
					return ErrCorrupt
				}
				o, live, err := s.getObservationReader(reader, row.SourceID)
				if err != nil {
					return err
				}
				if !live || o.AppID() != meetingAppID || o.Domain() != key+".meeting.local" || row.Bytes != len(o.Text()) {
					return ErrCorrupt
				}
				reverse, live, err := s.readIndexValueReader(reader, meetingSourceNS, row.SourceID, 64)
				matches := string(reverse) == key
				clear(reverse)
				if err != nil {
					return err
				}
				if !live || !matches {
					return ErrCorrupt
				}
				r := row.Record
				r.Note.Text = o.Text()
				if _, _, err := validateMeetingRecord(h, r); err != nil {
					return ErrCorrupt
				}
				stored := meetings.StoredRecord{Sequence: seq, Record: r, SourceID: row.SourceID}
				encoded := mustJSON(stored)
				size := len(encoded)
				clear(encoded)
				if size > maxBytes-used {
					if len(out.Records) == 0 {
						return meetings.ErrLimit
					}
					break
				}
				used += size
				out.Records = append(out.Records, stored)
			}
			seq--
		}
		out.Older = uint64(len(out.Records)) < h.Meeting.RecordCount
		if seq == 0 && out.Older {
			return ErrCorrupt
		}
		slices.Reverse(out.Records)
		return nil
	})
	if err != nil {
		return meetings.AgendaTail{}, err
	}
	return out, nil
}

func (s *Store) MeetingRecords(ctx context.Context, id string, p meetings.PageRequest) (meetings.RecordPage, error) {
	p, err := p.Normalized()
	if ctx == nil || err != nil || !meetingID(id, 64) {
		return meetings.RecordPage{}, meetings.ErrInvalid
	}
	out := meetings.RecordPage{Next: p.After}
	err = s.view(ctx, func(reader bolt.Reader) error {
		h, found, err := s.meetingHeaderReader(reader, id)
		if err != nil {
			return err
		}
		if !found {
			if fenced, err := s.meetingFenceReader(reader, meetingFenceNS, id); err != nil {
				return err
			} else if fenced {
				return meetings.ErrDeleted
			}
			return meetings.ErrNotFound
		}
		if p.After > h.LastSequence {
			return meetings.ErrInvalid
		}
		seq := p.After + 1
		used := 0
		for scanned := 0; seq <= h.LastSequence && scanned < meetingScanRows && len(out.Records) < p.Limit; scanned++ {
			raw, found, err := s.readIndexValueReader(reader, meetingOrderNS, meetingOrderID(id, seq), 64)
			key := string(raw)
			clear(raw)
			if err != nil {
				return err
			}
			if found {
				if !validHash(key) {
					return ErrCorrupt
				}
				row, live, err := s.meetingRowReader(reader, key)
				if err != nil {
					return err
				}
				if !live || row.Sequence != seq || row.Record.SessionID != id {
					return ErrCorrupt
				}
				o, live, err := s.getObservationReader(reader, row.SourceID)
				if err != nil {
					return err
				}
				if !live || o.AppID() != meetingAppID || o.Domain() != key+".meeting.local" {
					return ErrCorrupt
				}
				r := row.Record
				switch r.Kind {
				case meetings.TranscriptFinal:
					r.Event.Text = o.Text()
				case meetings.UserNote:
					r.Note.Text = o.Text()
				default:
					return ErrCorrupt
				}
				stored := meetings.StoredRecord{Sequence: seq, Record: r, SourceID: row.SourceID}
				size := len(mustJSON(stored))
				if used+size > p.MaxBytes {
					if len(out.Records) == 0 {
						return meetings.ErrLimit
					}
					break
				}
				used += size
				out.Records = append(out.Records, stored)
			}
			out.Next = seq
			seq++
		}
		out.More = seq <= h.LastSequence
		return nil
	})
	return out, err
}
