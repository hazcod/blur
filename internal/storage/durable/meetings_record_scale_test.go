package durable

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/meetings"
)

// Seed the ordinary encrypted record/source/order state in small transactions.
// Pending exact-index receipts intentionally model an interrupted Append; the
// public lookup/retry path must finish that source's existing work on demand.
func seedMeetingRecords(t *testing.T, s *Store, id string, records []meetings.Record) []meetings.Receipt {
	t.Helper()
	ctx := context.Background()
	receipts := make([]meetings.Receipt, len(records))
	for first := 0; first < len(records); first += 8 {
		if err := s.Update(ctx, func(tx *Tx) error {
			h, found, err := s.meetingHeaderReader(tx.writer, id)
			if err != nil || !found {
				if err != nil {
					return err
				}
				return meetings.ErrNotFound
			}
			for i := first; i < min(first+8, len(records)); i++ {
				r := records[i]
				text, at, err := validateMeetingRecord(h, r)
				if err != nil {
					return err
				}
				key := s.meetingDigest("key", []byte(r.Key))
				limit := meetings.DefaultLimits().MaxEventBytes
				if r.Kind == meetings.UserNote {
					limit = meetings.DefaultLimits().MaxNoteBytes
				}
				o, err := capture.SanitizeObservation(meetingAppID, key+".meeting.local", text, limit)
				if err != nil || o.Truncated() {
					return meetings.ErrInvalid
				}
				if err := tx.PutObservationAt(o, at); err != nil {
					return err
				}
				h.LastSequence++
				h.Meeting.RecordCount++
				encoded := mustJSON(r)
				row := meetingRecord{Sequence: h.LastSequence, Record: r, SourceID: o.ContentID(), Digest: s.meetingDigest("payload", encoded), Bytes: len(o.Text())}
				clear(encoded)
				row.Record.Event.Text, row.Record.Note.Text = "", ""
				if r.Kind == meetings.TranscriptFinal {
					h.Finals++
					h.TranscriptBytes += row.Bytes
				} else {
					h.Notes++
					h.ManualBytes += row.Bytes
				}
				for _, entry := range []struct {
					ns, key string
					value   []byte
				}{
					{meetingRowNS, key, mustJSON(row)},
					{meetingOrderNS, meetingOrderID(id, row.Sequence), []byte(key)},
					{meetingSourceNS, row.SourceID, []byte(key)},
				} {
					if err := tx.PutIndexRecord(entry.ns, entry.key, entry.value); err != nil {
						return err
					}
				}
				receipts[i].SourceID = row.SourceID
			}
			if err := tx.PutIndexRecord(meetingHeadNS, id, mustJSON(h)); err != nil {
				return err
			}
			// Leave the committed source's optional inline indexing unstarted,
			// exactly as a process exit after the primary transaction would.
			tx.mu.Lock()
			clear(tx.exactInline)
			tx.exactInline = nil
			tx.mu.Unlock()
			return nil
		}); err != nil {
			t.Fatalf("seed records at %d: %v", first, err)
		}
	}
	return receipts
}

func meetingScaleRecordID(index int) string { return fmt.Sprintf("record-%05d", index) }

func TestMeetingRecordsContinueBeyondHistoricalSessionTotals(t *testing.T) {
	ctx := context.Background()
	s, path, key := meetingStore(t)
	m := fixtureMeeting("large-session-continuation")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	// These actual encrypted sources/records reach all four former aggregate
	// boundaries: 32,768 finals/16MiB and 256 manual notes/1MiB. Small fixture
	// transactions model committed appends awaiting optional index completion.
	const finals, notes = 32768, 256
	transcript := "Historical transcript proof " + strings.Repeat("x", 512-len("Historical transcript proof "))
	manual := "Historical manual proof " + strings.Repeat("y", 4096-len("Historical manual proof "))
	records := make([]meetings.Record, 0, finals+notes)
	for i := 0; i < finals; i++ {
		r := fixtureMeetingFinal(m.ID, meetingScaleRecordID(i), transcript)
		r.Event.Start = time.Duration(i) * time.Millisecond
		r.Event.End = r.Event.Start + time.Millisecond
		records = append(records, r)
	}
	for i := 0; i < notes; i++ {
		id := fmt.Sprintf("note-%03d", i)
		records = append(records, meetings.Record{Key: m.ID + "/note/" + id, Kind: meetings.UserNote, SessionID: m.ID, Note: meetings.ManualNoteInput{ID: id, At: time.Duration(i) * time.Second, Text: manual}})
	}
	receipts := seedMeetingRecords(t, s, m.ID, records)
	continued := fixtureMeetingFinal(m.ID, "beyond-old-duration", transcript)
	continued.Event.Start, continued.Event.End = 9*time.Hour, 9*time.Hour+time.Second
	if _, err := s.Append(ctx, continued); err != nil {
		t.Fatal("accepted history stranded the next transcript record", err)
	}
	note := meetings.Record{Key: m.ID + "/note/beyond-old-totals", Kind: meetings.UserNote, SessionID: m.ID, Note: meetings.ManualNoteInput{ID: "beyond-old-totals", At: 10 * time.Hour, Text: manual}}
	if _, err := s.Append(ctx, note); err != nil {
		t.Fatal("accepted history stranded the next manual note", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	saved, found, err := s.GetMeeting(ctx, m.ID)
	if err != nil || !found || saved.RecordCount != 33026 {
		t.Fatalf("large session metadata did not survive reopen: %+v found=%v err=%v", saved, found, err)
	}
	got, found, err := s.LookupRecord(ctx, records[0])
	if err != nil || !found || got != receipts[0] {
		t.Fatalf("old evicted identity lost its durable receipt: %+v found=%v err=%v", got, found, err)
	}
	seen, after, firstCursor := 0, uint64(0), uint64(0)
	for {
		page, err := s.MeetingRecords(ctx, m.ID, meetings.PageRequest{After: after, MaxBytes: meetings.MaxPageBytes})
		if err != nil {
			t.Fatal("large session record paging failed", err)
		}
		for _, row := range page.Records {
			seen++
			if row.Sequence != uint64(seen) || row.SourceID == "" {
				t.Fatalf("large session paging omitted an accepted record: seen=%d row=%+v", seen, row)
			}
		}
		if firstCursor == 0 {
			firstCursor = page.Next
		}
		if !page.More {
			break
		}
		if page.Next <= after {
			t.Fatal("large session record cursor did not advance")
		}
		after = page.Next
	}
	if seen != 33026 {
		t.Fatalf("large session returned %d accepted records", seen)
	}
	if err := s.DeleteObservationRecord(ctx, receipts[50].SourceID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.LookupRecord(ctx, records[50]); found || !errors.Is(err, meetings.ErrDeleted) {
		t.Fatalf("large-session erasure lost its authoritative key fence: found=%v err=%v", found, err)
	}
	page, err := s.MeetingRecords(ctx, m.ID, meetings.PageRequest{After: firstCursor, Limit: 1})
	if err != nil || len(page.Records) != 1 || page.Records[0].Record.Key != records[51].Key {
		t.Fatalf("large session deletion changed its old record cursor: %+v err=%v", page, err)
	}
}

func TestMeetingHeaderAcceptsRepresentableAggregateTotals(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("aggregate-header")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	m.RecordCount = 33026
	h := meetingHeader{Meeting: m, Order: 1, LastSequence: 33026, Finals: 32769, Notes: 257, TranscriptBytes: (16 << 20) + 512, ManualBytes: (1 << 20) + 4096}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.PutIndexRecord(meetingHeadNS, m.ID, mustJSON(h)) }); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetMeeting(ctx, m.ID)
	if err != nil || !found || got.RecordCount != 33026 {
		t.Fatalf("representable accepted session totals became unreadable: %+v found=%v err=%v", got, found, err)
	}
}

func TestMeetingAppendChecksNaturalCounterBounds(t *testing.T) {
	ctx := context.Background()
	for _, field := range []string{"sequence", "transcript bytes", "manual bytes"} {
		t.Run(field, func(t *testing.T) {
			s, _, _ := meetingStore(t)
			m := fixtureMeeting("counter-bound")
			if err := s.PutMeeting(ctx, m, 0); err != nil {
				t.Fatal(err)
			}
			r := fixtureMeetingFinal(m.ID, "next", "Safe bounded payload")
			if err := s.Update(ctx, func(tx *Tx) error {
				h, _, err := s.meetingHeaderReader(tx.writer, m.ID)
				if err != nil {
					return err
				}
				switch field {
				case "sequence":
					h.LastSequence = 1<<48 - 1
				case "transcript bytes":
					h.TranscriptBytes = int(^uint(0) >> 1)
				case "manual bytes":
					h.ManualBytes = int(^uint(0) >> 1)
					r = meetings.Record{Key: m.ID + "/note/next", Kind: meetings.UserNote, SessionID: m.ID, Note: meetings.ManualNoteInput{ID: "next", At: time.Second, Text: "Safe bounded note"}}
				}
				return tx.PutIndexRecord(meetingHeadNS, m.ID, mustJSON(h))
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Append(ctx, r); !errors.Is(err, meetings.ErrLimit) {
				t.Fatalf("counter overflow was not rejected before mutation: %v", err)
			}
		})
	}
}
