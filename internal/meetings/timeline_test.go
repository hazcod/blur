package meetings

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRecordTimesSeparatesCreationRecordingAndManualClocks(t *testing.T) {
	created := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	recording := created.Add(24 * time.Hour)
	m := Meeting{ID: "timeline", StartedAt: created, RecordingStartedAt: recording}
	final := Record{Kind: TranscriptFinal, SessionID: m.ID, Event: Event{Kind: Final, SessionID: m.ID, Start: time.Second, End: 2 * time.Second}}
	start, end, err := RecordTimes(m, final)
	if err != nil || !start.Equal(recording.Add(time.Second)) || !end.Equal(recording.Add(2*time.Second)) {
		t.Fatal("transcript used the creation clock")
	}
	legacy := m
	legacy.RecordingStartedAt = time.Time{}
	start, _, err = RecordTimes(legacy, final)
	if err != nil || !start.Equal(created.Add(time.Second)) {
		t.Fatal("legacy transcript origin changed")
	}
	note := Record{Kind: UserNote, SessionID: m.ID, Note: ManualNoteInput{At: 48 * time.Hour}}
	start, end, err = RecordTimes(m, note)
	if err != nil || !start.Equal(created.Add(48*time.Hour)) || !end.IsZero() {
		t.Fatal("legacy note was rebased against recording or capped at8h")
	}
	note.Note.At = 0
	note.Note.RecordedAt = created.Add(72 * time.Hour)
	start, end, err = RecordTimes(m, note)
	if err != nil || !start.Equal(note.Note.RecordedAt) || !end.IsZero() {
		t.Fatal("explicit manual wall time changed")
	}
	note.Note.At = time.Second
	if _, _, err = RecordTimes(m, note); !errors.Is(err, ErrInvalid) {
		t.Fatal("ambiguous manual clocks silently selected")
	}
	final.Event.End = 40 * time.Hour
	if _, end, err = RecordTimes(m, final); err != nil || !end.Equal(recording.Add(40*time.Hour)) {
		t.Fatal("long audio offset rejected despite representable provenance", err)
	}
	final.Event.End = MaxCaptureDuration
	if _, _, err = RecordTimes(m, final); !errors.Is(err, ErrInvalid) {
		t.Fatal("unrepresentable transcript wall time was accepted")
	}
}
func TestManualNotesDoNotUseAudioDurationBudget(t *testing.T) {
	s, token, sink := newRunning(t, Limits{MaxDuration: time.Second})
	for _, note := range []ManualNoteInput{{ID: "later-legacy", At: 48 * time.Hour, Text: "Later note"}, {ID: "later-absolute", RecordedAt: time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC), Text: "Absolute later note"}} {
		if err := s.AddNote(context.Background(), note); err != nil {
			t.Fatalf("note inherited audio duration limit: %v", err)
		}
	}
	if len(sink.writes()) != 2 {
		t.Fatal("manual notes not saved separately")
	}
	if err := s.Submit(context.Background(), token, event("microphone", "too-late", 1, 0, 2*time.Second, "Audio still bounded", Final)); !errors.Is(err, ErrLimit) {
		t.Fatalf("transcript duration cap lost: %v", err)
	}
}
func TestManualAbsoluteTimeRetryIgnoresUnserializedMonotonicBits(t *testing.T) {
	s, _, sink := newRunning(t, Limits{})
	note := ManualNoteInput{ID: "same-time", RecordedAt: time.Now(), Text: "Exact retry evidence"}
	if err := s.AddNote(context.Background(), note); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(note)
	if err != nil {
		t.Fatal(err)
	}
	var reopened ManualNoteInput
	if err := json.Unmarshal(raw, &reopened); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNote(context.Background(), reopened); err != nil || len(sink.writes()) != 1 {
		t.Fatalf("canonical-equivalent note retry conflicted: %v", err)
	}
}
func TestRecordTimesRejectsOutOfRangeTimestampWithoutClamping(t *testing.T) {
	m := Meeting{ID: "timeline", StartedAt: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	for _, note := range []ManualNoteInput{{At: -time.Second}, {RecordedAt: time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC)}, {At: time.Duration(1<<63 - 1)}} {
		if _, _, err := RecordTimes(m, Record{Kind: UserNote, SessionID: m.ID, Note: note}); !errors.Is(err, ErrInvalid) {
			t.Fatal("unrepresentable evidence clock silently clamped")
		}
	}
}
