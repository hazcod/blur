package client

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestReadyAgendaContinuationRestoresLargeEncryptedHistoryAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	key := securestore.OfflineFixtureKey()
	store, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	metadata := meetings.Meeting{ID: "long-ready-agenda", Title: "Large saved agenda", Locale: "en-US", StartedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC), State: meetings.Ready, Tracks: []string{"microphone"}, Revision: 1}
	if err := store.PutMeeting(ctx, metadata, 0); err != nil {
		t.Fatal(err)
	}
	var deletedSource string
	for i := 0; i < 300; i++ {
		note := meetings.ManualNoteInput{ID: fmt.Sprintf("agenda-%03d", i), Text: fmt.Sprintf("Agenda %03d ", i) + strings.Repeat("n", 4096)}
		receipt, err := store.Append(ctx, meetings.Record{Key: metadata.ID + "/note/" + note.ID, SessionID: metadata.ID, Kind: meetings.UserNote, Note: note})
		if err != nil {
			t.Fatalf("saved note %d: %v", i, err)
		}
		if i == 290 {
			deletedSource = receipt.SourceID
		}
	}
	if err := store.DeleteObservationRecord(ctx, deletedSource); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	controller := newMeetingController(store, nil)
	active, tail, err := controller.restoreReadyAgenda(ctx, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.metadata.RecordCount != 299 || active.cursor != 300 || !active.tailOlder || len(tail) != 20 || meetingTailBytes(tail) > 128<<10 || tail[0].Record.Note.ID != "agenda-279" || tail[19].Record.Note.ID != "agenda-299" {
		t.Fatalf("large restored tail/cursor incorrect: count=%d cursor=%d tail=%d", active.metadata.RecordCount, active.cursor, len(tail))
	}
	// Read the complete evidence through the public bounded page API, including
	// records that have never been retained by the restored live view.
	cursor, count := uint64(0), 0
	for {
		page, err := store.MeetingRecords(ctx, metadata.ID, meetings.PageRequest{After: cursor, Limit: 17, MaxBytes: 64 << 10})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Records {
			index := count
			if index >= 290 {
				index++
			}
			want := fmt.Sprintf("agenda-%03d", index)
			if row.Record.Note.ID != want || row.SourceID == "" || row.Record.Note.Text != fmt.Sprintf("Agenda %03d ", index)+strings.Repeat("n", 4096) {
				t.Fatalf("full history row %d lost identity/text", count)
			}
			count++
		}
		if !page.More {
			break
		}
		if page.Next <= cursor {
			t.Fatal("full history cursor stalled")
		}
		cursor = page.Next
	}
	if count != 299 {
		t.Fatalf("full history has %d rows", count)
	}
	// Continue the same saved agenda after its old count and byte ceilings.
	note := meetings.ManualNoteInput{ID: "agenda-300", Text: "Continued after restart"}
	if _, err := store.Append(ctx, meetings.Record{Key: metadata.ID + "/note/" + note.ID, SessionID: metadata.ID, Kind: meetings.UserNote, Note: note}); err != nil {
		t.Fatal(err)
	}
	controller.active = active
	controller.view = MeetingLiveView{Meeting: active.metadata, Tail: tail, TailOlder: active.tailOlder}
	if err := controller.refreshTail(ctx, active); err != nil {
		t.Fatal(err)
	}
	live := controller.snapshot()
	if !live.TailOlder || len(live.Tail) != 20 || live.Tail[len(live.Tail)-1].Record.Note.ID != "agenda-300" {
		t.Fatal("append polling lost bounded tail or older-history marker")
	}
	active, tail, err = controller.restoreReadyAgenda(ctx, metadata.ID)
	if err != nil || active.cursor != 301 || !active.tailOlder || tail[len(tail)-1].Record.Note.ID != "agenda-300" {
		t.Fatal("continuation after restart failed", err)
	}
}

type indexedSparseAgendaRepo struct {
	meetings.Repository
	pages int
	row   meetings.StoredRecord
}

func (r *indexedSparseAgendaRepo) LookupRecord(ctx context.Context, record meetings.Record) (meetings.Receipt, bool, error) {
	return r.Repository.(meetings.RecordLookup).LookupRecord(ctx, record)
}
func (r *indexedSparseAgendaRepo) MeetingRecords(_ context.Context, _ string, p meetings.PageRequest) (meetings.RecordPage, error) {
	r.pages++
	if r.pages <= 300 {
		return meetings.RecordPage{Next: p.After + 1, More: true}, nil
	}
	row := r.row
	row.Sequence = p.After + 1
	return meetings.RecordPage{Records: []meetings.StoredRecord{row}, Next: row.Sequence}, nil
}
func TestReadyAgendaContinuationScansPastFormer257PageCeiling(t *testing.T) {
	f := newControllerFixture(t)
	ctx := context.Background()
	id, err := f.c.NewMeetingNotes(ctx, "Sparse agenda", "en-US", []string{"microphone"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.c.AddMeetingNote(ctx, id, meetings.ManualNoteInput{ID: "remaining", Text: "Evidence beyond erased slots"}); err != nil {
		t.Fatal(err)
	}
	page, err := f.store.MeetingRecords(ctx, id, meetings.PageRequest{})
	if err != nil || len(page.Records) != 1 {
		t.Fatal(err)
	}
	repo := &indexedSparseAgendaRepo{Repository: f.store, row: page.Records[0]}
	controller := newMeetingController(repo, nil)
	active, tail, err := controller.restoreReadyAgenda(ctx, id)
	if err != nil || repo.pages != 301 || active.cursor != 301 || len(tail) != 1 || tail[0].SourceID != page.Records[0].SourceID {
		t.Fatalf("sparse continuation lost evidence: pages=%d err=%v", repo.pages, err)
	}
}

func TestMeetingRecordingLengthSaturatesAtDurationRepresentationBoundary(t *testing.T) {
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	view := MeetingLiveView{RecordingElapsed: meetings.MaxCaptureDuration - 5*time.Nanosecond, AudioRecording: true, RecordingSince: now.Add(-10 * time.Nanosecond)}
	if got := view.RecordingLength(now); got != meetings.MaxCaptureDuration {
		t.Fatalf("recording clock overflowed: %v", got)
	}
	view.setAudioRecording(false, now)
	if view.RecordingElapsed != meetings.MaxCaptureDuration || !view.RecordingSince.IsZero() {
		t.Fatal("pause saved an overflowed recording clock")
	}
	view.setAudioRecording(true, now)
	if got := view.RecordingLength(now.Add(time.Second)); got != meetings.MaxCaptureDuration {
		t.Fatalf("resume overflowed an exhausted duration: %v", got)
	}
}

type boundedAgendaTailRepo struct {
	meetings.Repository
	result meetings.AgendaTail
	reads  int
}

func (r *boundedAgendaTailRepo) ReadyAgendaTail(context.Context, string, meetings.PageRequest) (meetings.AgendaTail, error) {
	r.reads++
	return r.result, nil
}
func (r *boundedAgendaTailRepo) GetMeeting(context.Context, string) (meetings.Meeting, bool, error) {
	panic("bounded restoration re-read metadata outside authenticated tail")
}
func (r *boundedAgendaTailRepo) MeetingRecords(context.Context, string, meetings.PageRequest) (meetings.RecordPage, error) {
	panic("bounded restoration scanned complete history")
}
func TestReadyAgendaContinuationUsesBoundedTailWithOpaqueAppendCursor(t *testing.T) {
	metadata := meetings.Meeting{ID: "sparse-agenda", Title: "Sparse agenda", Locale: "en-US", StartedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC), State: meetings.Ready, Tracks: []string{"microphone"}, Revision: 1, RecordCount: 2}
	row := meetings.StoredRecord{Sequence: 999999, SourceID: "saved-source", Record: meetings.Record{Kind: meetings.UserNote, SessionID: metadata.ID, Key: metadata.ID + "/note/remaining", Note: meetings.ManualNoteInput{ID: "remaining", Text: "Latest surviving agenda evidence"}}}
	repo := &boundedAgendaTailRepo{result: meetings.AgendaTail{Meeting: metadata, Records: []meetings.StoredRecord{row}, Cursor: 1000000, Older: true}}
	controller := newMeetingController(repo, nil)
	active, tail, err := controller.restoreReadyAgenda(context.Background(), metadata.ID)
	if err != nil || repo.reads != 1 || active.cursor != 1000000 || !active.tailOlder || len(tail) != 1 || tail[0] != row {
		t.Fatal("bounded ready tail lost its cursor/history marker", err)
	}
}
