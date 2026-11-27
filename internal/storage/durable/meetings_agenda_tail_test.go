package durable

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/meetings"
)

func TestReadyAgendaTailBoundsRowsWithoutChangingFullHistory(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("ready-agenda-tail")
	m.State = meetings.Ready
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	records := make([]meetings.Record, 32)
	for i := range records {
		id := fmt.Sprintf("note-%02d", i)
		records[i] = meetings.Record{Key: m.ID + "/note/" + id, Kind: meetings.UserNote, SessionID: m.ID, Note: meetings.ManualNoteInput{ID: id, At: time.Duration(i) * time.Second, Text: "Saved agenda history"}}
	}
	seedMeetingRecords(t, s, m.ID, records)
	reader, ok := any(s).(meetings.AgendaTailReader)
	if !ok {
		t.Fatal("encrypted repository cannot read an agenda with bounded startup work")
	}
	tail, err := reader.ReadyAgendaTail(ctx, m.ID, meetings.PageRequest{Limit: 50, MaxBytes: meetings.MaxPageBytes})
	if err != nil || len(tail.Records) != 20 || tail.Records[0].Record.Key != records[12].Key || tail.Records[19].Record.Key != records[31].Key || tail.Cursor != 32 || tail.Meeting.RecordCount != 32 || !tail.Older {
		t.Fatalf("bounded agenda tail was incomplete or mislabeled: %+v err=%v", tail, err)
	}
	page, err := s.MeetingRecords(ctx, m.ID, meetings.PageRequest{})
	if err != nil || len(page.Records) != 32 || page.Records[0].Record.Key != records[0].Key || page.More {
		t.Fatalf("agenda tail changed full history: count=%d more=%v err=%v", len(page.Records), page.More, err)
	}
}

func TestReadyAgendaTailBoundsDeletedOrdinalHolesAndPreservesAppendCursor(t *testing.T) {
	for _, last := range []uint64{100, 1 << 40} {
		t.Run(fmt.Sprint(last), func(t *testing.T) {
			ctx := context.Background()
			s, _, _ := meetingStore(t)
			m := fixtureMeeting("sparse-agenda-tail")
			m.State = meetings.Ready
			if err := s.PutMeeting(ctx, m, 0); err != nil {
				t.Fatal(err)
			}
			r := meetings.Record{Key: m.ID + "/note/one", Kind: meetings.UserNote, SessionID: m.ID, Note: meetings.ManualNoteInput{ID: "one", At: time.Second, Text: "An older live agenda note."}}
			seedMeetingRecords(t, s, m.ID, []meetings.Record{r})
			if err := s.Update(ctx, func(tx *Tx) error {
				h, _, err := s.meetingHeaderReader(tx.writer, m.ID)
				if err != nil {
					return err
				}
				h.LastSequence = last
				return tx.PutIndexRecord(meetingHeadNS, m.ID, mustJSON(h))
			}); err != nil {
				t.Fatal(err)
			}
			reader, ok := any(s).(meetings.AgendaTailReader)
			if !ok {
				t.Fatal("encrypted repository cannot read an agenda with bounded startup work")
			}
			bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			tail, err := reader.ReadyAgendaTail(bounded, m.ID, meetings.PageRequest{})
			if err != nil || tail.Cursor != last {
				t.Fatalf("sparse agenda required unbounded history work: %+v err=%v", tail, err)
			}
			if last == 100 && (len(tail.Records) != 1 || tail.Older) || last > 256 && (len(tail.Records) != 0 || !tail.Older) {
				t.Fatalf("agenda holes were mislabeled as live tail evidence: %+v", tail)
			}
		})
	}
}

func TestReadyAgendaTailBoundsSerializedBytesAndHonorsErasure(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("ready-large-note-tail")
	m.State = meetings.Ready
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	records := make([]meetings.Record, 3)
	for i := range records {
		id := fmt.Sprintf("note-%d", i)
		records[i] = meetings.Record{Key: m.ID + "/note/" + id, Kind: meetings.UserNote, SessionID: m.ID, Note: meetings.ManualNoteInput{ID: id, At: time.Duration(i) * time.Second, Text: strings.Repeat("x", 60<<10)}}
	}
	seedMeetingRecords(t, s, m.ID, records)
	reader, ok := any(s).(meetings.AgendaTailReader)
	if !ok {
		t.Fatal("encrypted repository cannot read an agenda with bounded startup work")
	}
	tail, err := reader.ReadyAgendaTail(ctx, m.ID, meetings.PageRequest{MaxBytes: meetings.MaxPageBytes})
	if err != nil || len(tail.Records) != 2 || tail.Records[0].Record.Key != records[1].Key || tail.Cursor != 3 || !tail.Older {
		t.Fatalf("agenda byte quantum was not bounded: rows=%d cursor=%d older=%v err=%v", len(tail.Records), tail.Cursor, tail.Older, err)
	}
	if err := s.DeleteMeeting(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadyAgendaTail(ctx, m.ID, meetings.PageRequest{}); !errors.Is(err, meetings.ErrDeleted) {
		t.Fatalf("erased agenda was restored: %v", err)
	}
}

func TestReadyAgendaTailAuthenticatesEligibilityBeforeRestore(t *testing.T) {
	ctx := context.Background()
	for _, state := range []string{"running", "recording origin", "ended", "prior final"} {
		t.Run(state, func(t *testing.T) {
			s, _, _ := meetingStore(t)
			m := fixtureMeeting("ineligible-agenda-tail")
			if state != "running" && state != "prior final" {
				m.State = meetings.Ready
			}
			if state == "recording origin" {
				m.RecordingStartedAt = m.StartedAt
			}
			if state == "ended" {
				m.EndedAt = m.StartedAt.Add(time.Hour)
			}
			if err := s.PutMeeting(ctx, m, 0); err != nil {
				t.Fatal(err)
			}
			if state == "prior final" {
				if _, err := s.Append(ctx, fixtureMeetingFinal(m.ID, "one", "A prior final.")); err != nil {
					t.Fatal(err)
				}
				m.State, m.Revision = meetings.Ready, 2
				if err := s.PutMeeting(ctx, m, 1); err != nil {
					t.Fatal(err)
				}
			}
			reader, ok := any(s).(meetings.AgendaTailReader)
			if !ok {
				t.Fatal("encrypted repository cannot read an agenda with bounded startup work")
			}
			if _, err := reader.ReadyAgendaTail(ctx, m.ID, meetings.PageRequest{}); !errors.Is(err, meetings.ErrState) {
				t.Fatalf("ineligible agenda was restored before audio admission: %v", err)
			}
		})
	}
}
