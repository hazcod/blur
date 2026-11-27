package durable

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/search"
)

func TestMeetingRecordLookupClassifiesOriginalPayloadAndDeletedKeys(t *testing.T) {
	ctx := context.Background()
	s, path, key := meetingStore(t)
	m := fixtureMeeting("authoritative-lookup")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	lookup, ok := any(s).(meetings.RecordLookup)
	if !ok {
		t.Fatal("encrypted repository cannot resolve stable record identities")
	}
	r := fixtureMeetingFinal(m.ID, "one", "Authorization: Bearer abcdefghijklmnop\n")
	if _, found, err := lookup.LookupRecord(ctx, r); err != nil || found {
		t.Fatalf("absent key was not absent: found=%v err=%v", found, err)
	}
	receipt, err := s.Append(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if got, found, err := lookup.LookupRecord(ctx, r); err != nil || !found || got != receipt {
		t.Fatalf("exact retry lost its receipt: got=%+v found=%v err=%v", got, found, err)
	}
	conflict := r
	conflict.Event.Text = "Authorization: Bearer ponmlkjihgfedcba\n"
	if _, found, err := lookup.LookupRecord(ctx, conflict); found || !errors.Is(err, meetings.ErrConflict) {
		t.Fatalf("redaction-equivalent original payload was accepted: found=%v err=%v", found, err)
	}
	if _, err := s.Append(ctx, fixtureMeetingFinal(m.ID, "two", "A second live record preserves session metadata.")); err != nil {
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
	lookup = any(s).(meetings.RecordLookup)
	if got, found, err := lookup.LookupRecord(ctx, r); err != nil || !found || got != receipt {
		t.Fatalf("encrypted reopen lost exact key identity: %+v %v %v", got, found, err)
	}
	if err := s.DeleteObservationRecord(ctx, receipt.SourceID); err != nil {
		t.Fatal(err)
	}
	for _, record := range []meetings.Record{r, conflict} {
		if _, found, err := lookup.LookupRecord(ctx, record); found || !errors.Is(err, meetings.ErrDeleted) {
			t.Fatalf("source erasure lost its stable-key fence: found=%v err=%v", found, err)
		}
	}
	if err := s.DeleteMeeting(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := lookup.LookupRecord(ctx, fixtureMeetingFinal(m.ID, "unseen", "Late evidence")); found || !errors.Is(err, meetings.ErrDeleted) {
		t.Fatalf("whole-session erasure allowed a late key: found=%v err=%v", found, err)
	}
}

func TestMeetingRecordLookupRejectsMisdirectedAuthenticatedSourceMembership(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("lookup-membership")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	r := fixtureMeetingFinal(m.ID, "one", "Original words.")
	receipt, err := s.Append(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	lookup, ok := any(s).(meetings.RecordLookup)
	if !ok {
		t.Fatal("encrypted repository cannot resolve stable record identities")
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		return tx.PutIndexRecord(meetingSourceNS, receipt.SourceID, []byte(s.meetingDigest("key", []byte("different/stable/key"))))
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := lookup.LookupRecord(ctx, r); found || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("misdirected source link granted a duplicate receipt: found=%v err=%v", found, err)
	}
}

func TestMeetingRecordLookupCompletesExistingPendingSearchReceipt(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("lookup-pending-index")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	r := fixtureMeetingFinal(m.ID, "one", "pendinglookupmarker retained immutable evidence")
	// Seed the exact committed state before Append's separate index completion.
	receipt := seedMeetingRecords(t, s, m.ID, []meetings.Record{r})[0]
	before, err := s.SearchExact(ctx, search.ExactQuery{Terms: []string{"pendinglookupmarker"}, TopK: 5, MaxRows: 10, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(before.Hits) != 0 {
		t.Fatalf("fixture did not retain unfinished exact-index work: hits=%+v err=%v", before.Hits, err)
	}
	lookup, ok := any(s).(meetings.RecordLookup)
	if !ok {
		t.Fatal("encrypted repository cannot resolve stable record identities")
	}
	got, found, err := lookup.LookupRecord(ctx, r)
	if err != nil || !found || got != receipt {
		t.Fatalf("pending committed retry did not finish: receipt=%+v found=%v err=%v", got, found, err)
	}
	hits, err := s.SearchExact(ctx, search.ExactQuery{Terms: []string{"pendinglookupmarker"}, TopK: 5, MaxRows: 10, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(hits.Hits) != 1 || hits.Hits[0].SourceID != receipt.SourceID {
		t.Fatalf("successful lookup left committed evidence unsearchable: hits=%+v err=%v", hits.Hits, err)
	}
}

func TestMeetingRecordLookupIgnoresUnserializedMonotonicClockBookkeeping(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("lookup-monotonic-time")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	note := meetings.Record{Key: m.ID + "/note/clock", Kind: meetings.UserNote, SessionID: m.ID, Note: meetings.ManualNoteInput{ID: "clock", RecordedAt: time.Now(), Text: "A note with a native monotonic timestamp."}}
	receipt, err := s.Append(ctx, note)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := s.LookupRecord(ctx, note)
	if err != nil || !found || got != receipt {
		t.Fatalf("same serialized payload was rejected after its clock bookkeeping was omitted: %+v %v %v", got, found, err)
	}
}
