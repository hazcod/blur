package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func fixtureMeeting(id string) meetings.Meeting {
	return meetings.Meeting{ID: id, Title: "Private launch planning", Locale: "en-US", StartedAt: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC), State: meetings.Running, Tracks: []string{"microphone", "system"}, Revision: 1}
}
func fixtureMeetingFinal(id, segment, text string) meetings.Record {
	return meetings.Record{Key: id + "/transcript/microphone/" + segment, Kind: meetings.TranscriptFinal, SessionID: id, Event: meetings.Event{Kind: meetings.Final, SessionID: id, TrackID: "microphone", SegmentID: segment, Revision: 3, Start: 120 * time.Millisecond, End: 980 * time.Millisecond, Text: text}}
}
func meetingStore(t *testing.T) (*Store, string, securestore.Key) {
	t.Helper()
	path, key := durableTestPath(t), durableTestKey(t, 0xC3)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path, key
}
func TestMeetingRepositoryEncryptedIncrementalReopen(t *testing.T) {
	ctx := context.Background()
	s, path, key := meetingStore(t)
	m := fixtureMeeting("meeting-a")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	if c := s.Capabilities(); c.Encryption != meetings.AuthenticatedEncryption || !c.AtomicIdempotent {
		t.Fatal("encrypted sink capability missing")
	}
	r := fixtureMeetingFinal(m.ID, "segment-one", "  The private decision is ivorytailmarker.\n")
	receipt, err := s.Append(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	dup, err := s.Append(ctx, r)
	if err != nil || dup != receipt {
		t.Fatalf("idempotent receipt mismatch: %v", err)
	}
	sameText := r
	sameText.Key = m.ID + "/transcript/microphone/segment-two"
	sameText.Event.SegmentID = "segment-two"
	second, err := s.Append(ctx, sameText)
	if err != nil || second.SourceID == receipt.SourceID {
		t.Fatalf("distinct segment collapsed: %v", err)
	}
	n := meetings.Record{Key: m.ID + "/note/manual-one", Kind: meetings.UserNote, SessionID: m.ID, Note: meetings.ManualNoteInput{ID: "manual-one", At: 2 * time.Second, Text: "  Manual agenda remains separate.\n"}}
	if _, err := s.Append(ctx, n); err != nil {
		t.Fatal(err)
	}
	page, err := s.MeetingRecords(ctx, m.ID, meetings.PageRequest{Limit: 2})
	if err != nil || len(page.Records) != 2 || !page.More {
		t.Fatalf("bounded record page: rows=%d more=%t err=%v", len(page.Records), page.More, err)
	}
	if page.Records[0].Record != r || page.Records[0].SourceID != receipt.SourceID {
		t.Fatal("exact transcript span or bytes lost")
	}
	tail, err := s.MeetingRecords(ctx, m.ID, meetings.PageRequest{After: page.Next, Limit: 2})
	if err != nil || len(tail.Records) != 1 || tail.Records[0].Record != n || tail.More {
		t.Fatalf("manual note paging: rows=%d err=%v", len(tail.Records), err)
	}
	hits, err := s.SearchExact(ctx, search.ExactQuery{Terms: []string{"ivorytailmarker"}, TopK: 5, MaxRows: 10, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(hits.Hits) != 2 {
		t.Fatalf("finals not immediately searchable: hits=%d err=%v", len(hits.Hits), err)
	}
	date, _ := localDate(m.StartedAt)
	today, found, err := s.GetTodaySnapshot(ctx, date)
	if err != nil || found || len(today.ConfirmedTodos) > 0 {
		t.Fatal("raw transcript polluted Today")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{m.Title, r.Event.Text, n.Note.Text, m.ID} {
		if bytes.Contains(raw, []byte(text)) {
			t.Fatal("meeting plaintext leaked to encrypted store")
		}
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	saved, found, err := s.GetMeeting(ctx, m.ID)
	if err != nil || !found || saved.RecordCount != 3 || saved.Title != m.Title {
		t.Fatalf("reopened metadata missing: found=%t count=%d err=%v", found, saved.RecordCount, err)
	}
	again, err := s.Append(ctx, r)
	if err != nil || again != receipt {
		t.Fatalf("reopened receipt mismatch: %v", err)
	}
	changed := r
	changed.Event.Text = "changed private evidence"
	if _, err := s.Append(ctx, changed); !errors.Is(err, meetings.ErrConflict) {
		t.Fatalf("conflicting stable key admitted: %v", err)
	}
}
func TestMeetingRepositoryRedactionAndOriginalPayloadConflict(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("redaction")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	r := fixtureMeetingFinal(m.ID, "one", "Authorization: Bearer abcdefghijklmnop\n")
	receipt, err := s.Append(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	source, found, err := s.GetObservation(ctx, receipt.SourceID)
	if err != nil || !found || source.Text() == r.Event.Text || source.Truncated() {
		t.Fatal("meeting text was not safely redacted")
	}
	different := r
	different.Event.Text = "Authorization: Bearer ponmlkjihgfedcba\n"
	if _, err := s.Append(ctx, different); !errors.Is(err, meetings.ErrConflict) {
		t.Fatalf("redaction-equivalent payload bypassed conflict: %v", err)
	}
}
func TestMeetingRepositorySourceEraseFencesAndPrunes(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("source-erase")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	a := fixtureMeetingFinal(m.ID, "a", "ceruleanerase first")
	b := fixtureMeetingFinal(m.ID, "b", "ceruleanerase second")
	receipt, err := s.Append(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Append(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObservationRecord(ctx, receipt.SourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, a); !errors.Is(err, meetings.ErrDeleted) {
		t.Fatalf("erased record resurrected: %v", err)
	}
	page, err := s.MeetingRecords(ctx, m.ID, meetings.PageRequest{})
	if err != nil || len(page.Records) != 1 || page.Records[0].SourceID != second.SourceID {
		t.Fatalf("erased record metadata retained: rows=%d err=%v", len(page.Records), err)
	}
	if err := s.DeleteObservationRecord(ctx, second.SourceID); err != nil {
		t.Fatal(err)
	}
	_, found, err := s.GetMeeting(ctx, m.ID)
	if err != nil || found {
		t.Fatalf("last-source metadata survived: found=%t err=%v", found, err)
	}
	m.Revision = 2
	if err := s.PutMeeting(ctx, m, 1); !errors.Is(err, meetings.ErrDeleted) {
		t.Fatalf("erased meeting resurrected: %v", err)
	}
}
func TestMeetingRepositoryDeleteResumesBeforeServing(t *testing.T) {
	ctx := context.Background()
	s, path, key := meetingStore(t)
	m := fixtureMeeting("delete-resume")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, fixtureMeetingFinal(m.ID, fmt.Sprintf("segment-%d", i), "deletiontailmarker private evidence")); err != nil {
			t.Fatal(err)
		}
	}
	interrupted := errors.New("synthetic interrupted deletion")
	if err := s.deleteMeetingWithHook(ctx, m.ID, func() error { return interrupted }); !errors.Is(err, interrupted) {
		t.Fatalf("expected interrupted deletion: %v", err)
	}
	if _, _, err := s.GetMeeting(ctx, m.ID); !errors.Is(err, ErrDeletionPending) {
		t.Fatalf("partial deletion exposed: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_, found, err := s.GetMeeting(ctx, m.ID)
	if err != nil || found {
		t.Fatalf("deleted meeting reappeared: found=%t err=%v", found, err)
	}
	result, err := s.SearchExact(ctx, search.ExactQuery{Terms: []string{"deletiontailmarker"}, TopK: 5, MaxRows: 10, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(result.Hits) != 0 {
		t.Fatalf("deleted text searchable: hits=%d err=%v", len(result.Hits), err)
	}
	if _, err := s.Append(ctx, fixtureMeetingFinal(m.ID, "segment-0", "deletiontailmarker private evidence")); !errors.Is(err, meetings.ErrDeleted) {
		t.Fatalf("restart resurrected deleted receipt: %v", err)
	}
	if err := s.DeleteMeeting(ctx, m.ID); err != nil {
		t.Fatalf("delete replay failed: %v", err)
	}
}
func TestMeetingRepositoryMetadataRevisionPagingAndCleanup(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	for i := 0; i < 4; i++ {
		m := fixtureMeeting(fmt.Sprintf("meeting-%d", i))
		m.StartedAt = m.StartedAt.Add(time.Duration(i) * time.Hour)
		if err := s.PutMeeting(ctx, m, 0); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.ListMeetings(ctx, meetings.PageRequest{Limit: 2})
	if err != nil || len(p.Meetings) != 2 || !p.More || p.Meetings[0].ID != "meeting-3" {
		t.Fatalf("newest first page: rows=%d err=%v", len(p.Meetings), err)
	}
	next, err := s.ListMeetings(ctx, meetings.PageRequest{Limit: 2, After: p.Next})
	if err != nil || len(next.Meetings) != 2 || next.More {
		t.Fatalf("next metadata page: rows=%d err=%v", len(next.Meetings), err)
	}
	m := fixtureMeeting("meeting-0")
	m.Title = "Renamed agenda"
	m.Revision = 2
	if err := s.PutMeeting(ctx, m, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMeeting(ctx, m, 1); !errors.Is(err, meetings.ErrConflict) {
		t.Fatalf("metadata revision conflict missing: %v", err)
	}
	removed, err := s.DeleteMeetingsBefore(ctx, fixtureMeeting("x").StartedAt.Add(time.Hour))
	if err != nil || removed != 1 {
		t.Fatalf("empty metadata retention: removed=%d err=%v", removed, err)
	}
	removed, err = s.DeleteAllMeetings(ctx)
	if err != nil || removed != 3 {
		t.Fatalf("empty metadata full erase: removed=%d err=%v", removed, err)
	}
	all, err := s.ListMeetings(ctx, meetings.PageRequest{})
	if err != nil || len(all.Meetings) != 0 {
		t.Fatalf("full erase left metadata: rows=%d err=%v", len(all.Meetings), err)
	}
}
func TestMeetingRepositoryLargeFinalTailSearchAndByteBudget(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("long-final")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&text, "word%04d ", i)
	}
	text.WriteString("ultramarinetailmarker")
	r := fixtureMeetingFinal(m.ID, "one", text.String())
	if _, err := s.Append(ctx, r); err != nil {
		t.Fatal(err)
	}
	result, err := s.SearchExact(ctx, search.ExactQuery{Terms: []string{"ultramarinetailmarker"}, TopK: 1, MaxRows: 10, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(result.Hits) != 1 {
		t.Fatalf("multi-batch final tail missing: hits=%d err=%v", len(result.Hits), err)
	}
	_, err = s.MeetingRecords(ctx, m.ID, meetings.PageRequest{MaxBytes: 256})
	if !errors.Is(err, meetings.ErrLimit) {
		t.Fatalf("oversized page silently truncated or stalled: %v", err)
	}
}

func TestMeetingRepositoryRetentionPreservesNewerSegmentsAndFencesOldRetry(t *testing.T) {
	ctx := context.Background()
	s, path, key := meetingStore(t)
	m := fixtureMeeting("retained-session")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	old := fixtureMeetingFinal(m.ID, "old", "oldretentionmarker")
	newer := fixtureMeetingFinal(m.ID, "new", "newretentionmarker")
	newer.Event.Start = 2 * time.Second
	newer.Event.End = 3 * time.Second
	oldReceipt, err := s.Append(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	newReceipt, err := s.Append(ctx, newer)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := m.StartedAt.Add(2 * time.Second)
	removed, err := s.DeleteObservationRecordBefore(ctx, oldReceipt.SourceID, cutoff, false, nil)
	if err != nil || !removed {
		t.Fatalf("old record not retained correctly: %v", err)
	}
	removed, err = s.DeleteObservationRecordBefore(ctx, newReceipt.SourceID, cutoff, false, nil)
	if err != nil || removed {
		t.Fatalf("new record deleted early: %v", err)
	}
	count, err := s.DeleteMeetingsBefore(ctx, cutoff)
	if err != nil || count != 0 {
		t.Fatalf("retention erased meeting with newer evidence: %v", err)
	}
	page, err := s.MeetingRecords(ctx, m.ID, meetings.PageRequest{})
	if err != nil || len(page.Records) != 1 || page.Records[0].SourceID != newReceipt.SourceID {
		t.Fatalf("newer transcript missing: rows=%d err=%v", len(page.Records), err)
	}
	var captured time.Time
	err = s.view(ctx, func(r bolt.Reader) error {
		var err error
		captured, err = s.observationCapturedAtReader(r, newReceipt.SourceID)
		return err
	})
	if err != nil || !captured.Equal(m.StartedAt.Add(newer.Event.End)) {
		t.Fatal("native time span not converted exactly")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.Append(ctx, old); !errors.Is(err, meetings.ErrDeleted) {
		t.Fatalf("retention replay recreated text: %v", err)
	}
	if err := s.DeleteObservationRecord(ctx, newReceipt.SourceID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetMeeting(ctx, m.ID); err != nil || found {
		t.Fatalf("final-source retention left metadata: found=%t err=%v", found, err)
	}
}
func TestMeetingRepositoryRejectsInvalidOrTruncatedEvidenceAtomically(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("invalid-records")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	base := fixtureMeetingFinal(m.ID, "one", "valid evidence")
	mutations := []func(*meetings.Record){
		func(r *meetings.Record) { r.Event.Kind = meetings.Partial },
		func(r *meetings.Record) { r.Event.TrackID = "unregistered" },
		func(r *meetings.Record) { r.Event.Revision = 0 },
		func(r *meetings.Record) { r.Event.Start = -time.Second },
		func(r *meetings.Record) { r.Event.End = 9 * time.Hour },
		func(r *meetings.Record) { r.Key = "different-key" },
		func(r *meetings.Record) { r.Event.Text = strings.Repeat("x", meetings.DefaultLimits().MaxEventBytes+1) },
		// Redaction expands each SSN, so the sanitized text exceeds the event cap.
		func(r *meetings.Record) {
			r.Event.Text = strings.Repeat("123-45-6789 ", meetings.DefaultLimits().MaxEventBytes/12)
		},
	}
	for i, mutate := range mutations {
		t.Run(fmt.Sprintf("invalid-%d", i), func(t *testing.T) {
			r := base
			mutate(&r)
			if _, err := s.Append(ctx, r); !errors.Is(err, meetings.ErrInvalid) {
				t.Fatalf("invalid evidence admitted: %v", err)
			}
		})
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Append(canceled, base); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled append admitted: %v", err)
	}
	mStored, found, err := s.GetMeeting(ctx, m.ID)
	if err != nil || !found || mStored.RecordCount != 0 {
		t.Fatalf("failed append changed metadata: count=%d err=%v", mStored.RecordCount, err)
	}
	// Metadata CAS cannot move already linked spans by changing the start clock.
	m.StartedAt = m.StartedAt.Add(time.Second)
	m.Revision = 2
	if err := s.PutMeeting(ctx, m, 1); !errors.Is(err, meetings.ErrInvalid) {
		t.Fatalf("meeting origin rewritten: %v", err)
	}
}
func TestMeetingRepositoryEmptyTailCursorAndStoppedManualNotes(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("stopped-manual")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	r := fixtureMeetingFinal(m.ID, "one", "last spoken segment")
	if _, err := s.Append(ctx, r); err != nil {
		t.Fatal(err)
	}
	first, err := s.MeetingRecords(ctx, m.ID, meetings.PageRequest{})
	if err != nil || first.More || first.Next != 1 {
		t.Fatal("last consumed cursor lost")
	}
	empty, err := s.MeetingRecords(ctx, m.ID, meetings.PageRequest{After: first.Next})
	if err != nil || empty.Next != first.Next || len(empty.Records) != 0 || empty.More {
		t.Fatal("empty tail reset cursor")
	}
	m.State = meetings.Stopped
	m.EndedAt = m.StartedAt.Add(time.Minute)
	m.Revision = 2
	if err := s.PutMeeting(ctx, m, 1); err != nil {
		t.Fatal(err)
	}
	note := meetings.Record{Key: m.ID + "/note/followup", Kind: meetings.UserNote, SessionID: m.ID, Note: meetings.ManualNoteInput{ID: "followup", At: time.Minute, Text: "Post-meeting manual correction."}}
	if _, err := s.Append(ctx, note); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, r); err != nil {
		t.Fatalf("valid retry after Stop rejected: %v", err)
	}
	late := fixtureMeetingFinal(m.ID, "late", "unauthorized late final")
	if _, err := s.Append(ctx, late); !errors.Is(err, meetings.ErrState) {
		t.Fatalf("late new final after Stop persisted: %v", err)
	}
	tail, err := s.MeetingRecords(ctx, m.ID, meetings.PageRequest{After: empty.Next})
	if err != nil || len(tail.Records) != 1 || tail.Records[0].Record.Kind != meetings.UserNote || tail.Next != 2 {
		t.Fatal("incremental manual tail missing")
	}
}
func TestMeetingRepositoryDeletionRecoveryAtEverySourcePhase(t *testing.T) {
	for _, phase := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 14} {
		t.Run(fmt.Sprintf("phase-%d", phase), func(t *testing.T) {
			ctx := context.Background()
			s, path, key := meetingStore(t)
			m := fixtureMeeting("phase-recovery")
			if err := s.PutMeeting(ctx, m, 0); err != nil {
				t.Fatal(err)
			}
			r := fixtureMeetingFinal(m.ID, "one", "phaseerasuremarker private source")
			receipt, err := s.Append(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			interrupted := errors.New("synthetic interruption")
			err = s.deleteMeetingWithHook(ctx, m.ID, func() error {
				j, found, err := s.readSourceErasureLocked(ctx)
				if err != nil {
					return err
				}
				if found && j.Phase == phase {
					return interrupted
				}
				return nil
			})
			if !errors.Is(err, interrupted) {
				t.Fatalf("phase interruption was not reached: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenDurable(path, key, OpenExisting)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if _, found, err := s.GetObservation(ctx, receipt.SourceID); err != nil || found {
				t.Fatalf("source survived recovery: found=%t err=%v", found, err)
			}
			if _, found, err := s.GetMeeting(ctx, m.ID); err != nil || found {
				t.Fatalf("metadata survived recovery: found=%t err=%v", found, err)
			}
			if _, err := s.Append(ctx, r); !errors.Is(err, meetings.ErrDeleted) {
				t.Fatalf("recovered key resurrected: %v", err)
			}
		})
	}
}

func TestMeetingRepositoryConcurrentRetryCommitsOneReceipt(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("concurrent-retry")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	r := fixtureMeetingFinal(m.ID, "one", "concurrency receipt marker")
	type result struct {
		receipt meetings.Receipt
		err     error
	}
	results := make(chan result, 16)
	for i := 0; i < 16; i++ {
		go func() { receipt, err := s.Append(ctx, r); results <- result{receipt, err} }()
	}
	var source string
	for i := 0; i < 16; i++ {
		out := <-results
		if out.err != nil {
			t.Fatal(out.err)
		}
		if i == 0 {
			source = out.receipt.SourceID
		} else if out.receipt.SourceID != source {
			t.Fatal("concurrent key received different sources")
		}
	}
	mStored, found, err := s.GetMeeting(ctx, m.ID)
	if err != nil || !found || mStored.RecordCount != 1 {
		t.Fatalf("concurrent retry duplicated evidence: count=%d err=%v", mStored.RecordCount, err)
	}
	page, err := s.MeetingRecords(ctx, m.ID, meetings.PageRequest{})
	if err != nil || len(page.Records) != 1 {
		t.Fatalf("duplicate record rows: rows=%d err=%v", len(page.Records), err)
	}
}
func TestMeetingRepositoryEraseAllFencesDelayedMetadataCreation(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("delayed-metadata")
	if err := s.RecordTombstone(ctx, Tombstone{SourceID: AllSourcesTombstone, At: m.StartedAt.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMeeting(ctx, m, 0); !errors.Is(err, meetings.ErrDeleted) {
		t.Fatalf("old metadata recreated after global erasure: %v", err)
	}
	m.ID = "new-after-erase"
	m.StartedAt = m.StartedAt.Add(time.Minute)
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
}
