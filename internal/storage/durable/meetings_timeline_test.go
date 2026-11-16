package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func sourceClock(t *testing.T, s *Store, id string) time.Time {
	t.Helper()
	var at time.Time
	err := s.view(context.Background(), func(r bolt.Reader) error { var err error; at, err = s.observationCapturedAtReader(r, id); return err })
	if err != nil {
		t.Fatal(err)
	}
	return at
}
func TestMeetingAgendaFromYesterdayGetsSeparateImmutableRecordingOrigin(t *testing.T) {
	ctx := context.Background()
	s, path, key := meetingStore(t)
	m := fixtureMeeting("yesterday-agenda")
	m.State = meetings.Ready
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	agenda := meetings.Record{Key: m.ID + "/note/agenda", SessionID: m.ID, Kind: meetings.UserNote, Note: meetings.ManualNoteInput{ID: "agenda", Text: "  Yesterday's agenda remains exactly saved.\n"}}
	agendaReceipt, err := s.Append(ctx, agenda)
	if err != nil {
		t.Fatal(err)
	}
	oldClock := sourceClock(t, s, agendaReceipt.SourceID)
	m.RecordingStartedAt = m.StartedAt.Add(24 * time.Hour)
	m.Revision = 2
	if err := s.PutMeeting(ctx, m, 1); err != nil {
		t.Fatal(err)
	}
	m.State = meetings.Running
	m.Revision = 3
	if err := s.PutMeeting(ctx, m, 2); err != nil {
		t.Fatal(err)
	}
	final := fixtureMeetingFinal(m.ID, "first", "Today’s first recorded final.")
	receipt, err := s.Append(ctx, final)
	if err != nil {
		t.Fatal(err)
	}
	if at := sourceClock(t, s, receipt.SourceID); !at.Equal(m.RecordingStartedAt.Add(final.Event.End)) {
		t.Fatal("first final inherited yesterday's creation offset")
	}
	if at := sourceClock(t, s, agendaReceipt.SourceID); !at.Equal(oldClock) {
		t.Fatal("starting recording rewrote old agenda source time")
	}
	if duplicate, err := s.Append(ctx, agenda); err != nil || duplicate != agendaReceipt {
		t.Fatalf("starting recording broke old agenda receipt: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	saved, found, err := s.GetMeeting(ctx, m.ID)
	if err != nil || !found || !saved.RecordingStartedAt.Equal(m.RecordingStartedAt) {
		t.Fatal("recording origin lost on encrypted reopen")
	}
	page, err := s.MeetingRecords(ctx, m.ID, meetings.PageRequest{})
	if err != nil || len(page.Records) != 2 || page.Records[0].Record != agenda || page.Records[1].Record != final {
		t.Fatalf("starting recording rewrote saved provenance: rows=%d err=%v", len(page.Records), err)
	}
	changed := saved
	changed.RecordingStartedAt = changed.RecordingStartedAt.Add(time.Second)
	changed.Revision++
	if err := s.PutMeeting(ctx, changed, saved.Revision); !errors.Is(err, meetings.ErrInvalid) {
		t.Fatalf("frozen recording origin changed: %v", err)
	}
}
func TestMeetingLaterManualNotesHaveIndependentRecordedTime(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("later-manual")
	m.State = meetings.Stopped
	m.EndedAt = m.StartedAt.Add(time.Hour)
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		id       string
		at       time.Duration
		recorded time.Time
		expected time.Time
	}{
		{"legacy-later", 48 * time.Hour, time.Time{}, m.StartedAt.Add(48 * time.Hour)},
		{"absolute-later", 0, m.StartedAt.Add(72 * time.Hour), m.StartedAt.Add(72 * time.Hour)},
		// A draft is captured immediately before its new meeting header is created.
		{"predates-create", 0, m.StartedAt.Add(-time.Millisecond), m.StartedAt.Add(-time.Millisecond)},
	} {
		note := meetings.Record{Key: m.ID + "/note/" + test.id, SessionID: m.ID, Kind: meetings.UserNote, Note: meetings.ManualNoteInput{ID: test.id, At: test.at, RecordedAt: test.recorded, Text: "Exact manual note on its own wall clock."}}
		receipt, err := s.Append(ctx, note)
		if err != nil {
			t.Fatalf("manual note inherited audio duration cap: %v", err)
		}
		if at := sourceClock(t, s, receipt.SourceID); !at.Equal(test.expected) {
			t.Fatal("manual note wall timestamp was clamped or rebased")
		}
	}
	invalid := meetings.Record{Key: m.ID + "/note/ambiguous", SessionID: m.ID, Kind: meetings.UserNote, Note: meetings.ManualNoteInput{ID: "ambiguous", At: time.Second, RecordedAt: m.StartedAt.Add(time.Hour), Text: "Ambiguous clocks must be explicit."}}
	if _, err := s.Append(ctx, invalid); !errors.Is(err, meetings.ErrInvalid) {
		t.Fatalf("two independent note clocks accepted: %v", err)
	}
}
func TestMeetingLegacyFinalOriginCannotBeRebased(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("legacy-final")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	r := fixtureMeetingFinal(m.ID, "one", "Legacy source keeps its exact timestamp.")
	receipt, err := s.Append(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	original := sourceClock(t, s, receipt.SourceID)
	m.RecordingStartedAt = m.StartedAt.Add(time.Hour)
	m.Revision = 2
	if err := s.PutMeeting(ctx, m, 1); !errors.Is(err, meetings.ErrInvalid) {
		t.Fatalf("legacy transcript clock rebased: %v", err)
	}
	if !sourceClock(t, s, receipt.SourceID).Equal(original) {
		t.Fatal("legacy source time changed")
	}
}

// These frozen wire structs match headers/records emitted before the optional
// fields existed. Exact JSON equality also establishes unchanged HMAC inputs.
type legacyMeetingWire struct {
	ID, Title, Locale     string
	StartedAt, EndedAt    time.Time
	State                 meetings.State
	Tracks                []string
	Revision, RecordCount uint64
}
type legacyNoteWire struct {
	ID   string
	At   time.Duration
	Text string
}
type legacyRecordWire struct {
	Key       string
	Kind      meetings.RecordKind
	SessionID string
	Event     meetings.Event
	Note      legacyNoteWire
}
type legacyMeetingHeaderWire struct {
	Meeting                      legacyMeetingWire
	Order, LastSequence          uint64
	Finals, Notes                int
	TranscriptBytes, ManualBytes int
}
type legacyMeetingRowWire struct {
	Sequence         uint64
	Record           legacyRecordWire
	SourceID, Digest string
	Bytes            int
}

func legacyMetadata(m meetings.Meeting) legacyMeetingWire {
	return legacyMeetingWire{m.ID, m.Title, m.Locale, m.StartedAt, m.EndedAt, m.State, m.Tracks, m.Revision, m.RecordCount}
}
func legacyRecord(r meetings.Record) legacyRecordWire {
	return legacyRecordWire{r.Key, r.Kind, r.SessionID, r.Event, legacyNoteWire{r.Note.ID, r.Note.At, r.Note.Text}}
}
func TestMeetingOptionalClockFieldsPreserveLegacyCanonicalHeadersAndHMAC(t *testing.T) {
	ctx := context.Background()
	s, path, key := meetingStore(t)
	m := fixtureMeeting("legacy-canonical")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	r := meetings.Record{Key: m.ID + "/note/one", SessionID: m.ID, Kind: meetings.UserNote, Note: meetings.ManualNoteInput{ID: "one", At: time.Second, Text: "Legacy exact note bytes.\n"}}
	oldPayload, newPayload := mustJSON(legacyRecord(r)), mustJSON(r)
	if !bytes.Equal(oldPayload, newPayload) {
		t.Fatal("optional clock field changed original payload/HMAC bytes")
	}
	receipt, err := s.Append(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		h, found, err := s.meetingHeaderReader(tx.writer, m.ID)
		if err != nil || !found {
			t.Fatal("fixture header missing")
		}
		encoded := mustJSON(legacyMeetingHeaderWire{legacyMetadata(h.Meeting), h.Order, h.LastSequence, h.Finals, h.Notes, h.TranscriptBytes, h.ManualBytes})
		if !bytes.Equal(encoded, mustJSON(h)) {
			t.Fatal("optional clock field changed legacy header bytes")
		}
		if err := tx.putIndexValueLocked(meetingHeadNS, m.ID, encoded); err != nil {
			return err
		}
		key := s.meetingDigest("key", []byte(r.Key))
		row, found, err := s.meetingRowReader(tx.writer, key)
		if err != nil || !found {
			t.Fatal("fixture row missing")
		}
		if row.Digest != s.meetingDigest("payload", oldPayload) {
			t.Fatal("legacy original payload digest changed")
		}
		wire := legacyMeetingRowWire{row.Sequence, legacyRecord(row.Record), row.SourceID, row.Digest, row.Bytes}
		return tx.putIndexValueLocked(meetingRowNS, key, mustJSON(wire))
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if duplicate, err := s.Append(ctx, r); err != nil || duplicate != receipt {
		t.Fatalf("legacy receipt no longer replays: %v", err)
	}
	if saved, found, err := s.GetMeeting(ctx, m.ID); err != nil || !found || !saved.RecordingStartedAt.IsZero() || !saved.TranscriptOrigin().Equal(m.StartedAt) {
		t.Fatal("legacy origin did not survive canonical reopen")
	}
	var row meetingRecord
	err = s.view(ctx, func(reader bolt.Reader) error {
		var err error
		row, _, err = s.meetingRowReader(reader, s.meetingDigest("key", []byte(r.Key)))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(row.Record)
	if bytes.Contains(encoded, []byte("RecordedAt")) {
		t.Fatal("zero optional field was encoded into old record")
	}
}
