package client

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

type meetingDeleteNotices struct {
	clientNoticePort
	removeError error
	allCalls    int
	removeCalls [][]string
}

func (p *meetingDeleteNotices) Remove(ctx context.Context, ids []string) error {
	p.removeCalls = append(p.removeCalls, append([]string(nil), ids...))
	if p.removeError != nil {
		return p.removeError
	}
	return p.clientNoticePort.Remove(ctx, ids)
}

func (p *meetingDeleteNotices) RemoveAll(ctx context.Context) error {
	p.allCalls++
	if p.removeError != nil {
		return p.removeError
	}
	return p.clientNoticePort.RemoveAll(ctx)
}

func TestDeleteMeetingCancelsNativeLinkedRemindersBeforeErasingEvidence(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	m := meetings.Meeting{ID: "meeting-delete-fixture", Title: "Launch review", Locale: "en-US", StartedAt: now, State: meetings.Running, Tracks: []string{"microphone"}, Revision: 1}
	if err := store.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	row, err := store.Append(ctx, meetings.Record{Key: m.ID + "/transcript/microphone/final-one", SessionID: m.ID, Kind: meetings.TranscriptFinal,
		Event: meetings.Event{Kind: meetings.Final, SessionID: m.ID, TrackID: "microphone", SegmentID: "final-one", Revision: 1, Start: 0, End: time.Second, Text: "Mira owns the rollback."}})
	if err != nil {
		t.Fatal(err)
	}
	port := &meetingDeleteNotices{}
	service, err := reminders.NewService(store, port, reminders.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	c := NewClient(store)
	c.reminders = service
	defer c.Close()
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleter.SetNotificationCleanup(service)
	c.WithDeletion(deleter)
	due := &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "UTC"}
	linked, err := service.Create(ctx, "Review rollback", due, row.SourceID, false)
	if err != nil || linked.ScheduleError != nil {
		t.Fatal(linked, err)
	}
	unrelated, err := service.Create(ctx, "Collect groceries", due, "", false)
	if err != nil || unrelated.ScheduleError != nil {
		t.Fatal(unrelated, err)
	}
	if len(port.pending) != 2 {
		t.Fatal("fixture did not queue both notices", port.pending)
	}
	unrelatedID := reminders.NotificationID(unrelated.Item)
	beforeUnrelated := port.pending[unrelatedID]
	port.removeError = errors.New("fixture notification removal failed")
	if err := c.DeleteMeeting(ctx, m.ID); !errors.Is(err, port.removeError) {
		t.Fatalf("meeting deletion ignored failed native cancellation: %v", err)
	}
	if _, found, err := store.GetMeeting(ctx, m.ID); err != nil || !found {
		t.Fatalf("meeting erased despite failed cancellation: found=%t err=%v", found, err)
	}
	if _, found, err := store.GetObservation(ctx, row.SourceID); err != nil || !found {
		t.Fatalf("source erased despite failed cancellation: found=%t err=%v", found, err)
	}
	if log, err := store.Tombstones(ctx); err != nil || len(log) != 0 {
		t.Fatalf("failed native guard recorded a deletion intent: %+v, %v", log, err)
	}
	if port.allCalls != 0 || len(port.pending) != 2 || len(port.removeCalls) != 1 || !slices.Equal(port.removeCalls[0], []string{reminders.NotificationID(linked.Item)}) {
		t.Fatalf("failed targeted cancellation touched unrelated native notices: calls=%v all=%d pending=%v", port.removeCalls, port.allCalls, port.pending)
	}
	port.removeError = nil
	if err := c.DeleteMeeting(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetTask(ctx, linked.Item.ID); err != nil || found {
		t.Fatalf("linked task survived meeting deletion: found=%t err=%v", found, err)
	}
	if _, found, err := store.GetTask(ctx, unrelated.Item.ID); err != nil || !found {
		t.Fatalf("unrelated task was erased: found=%t err=%v", found, err)
	}
	for pass := 0; pass < 8; pass++ {
		err := service.Reconcile(ctx)
		if err == nil {
			break
		}
		if !errors.Is(err, reminders.ErrMoreWork) || pass == 7 {
			t.Fatal(err)
		}
	}
	if len(port.pending) != 1 {
		t.Fatalf("deleted reminder was requeued or unrelated reminder lost: %+v", port.pending)
	}
	for _, notice := range port.pending {
		if notice.ID != reminders.NotificationID(unrelated.Item) {
			t.Fatalf("deleted meeting reminder remained scheduled: %+v", notice)
		}
	}
	if port.allCalls != 0 || port.queued != 2 || !reflect.DeepEqual(port.pending[unrelatedID], beforeUnrelated) {
		t.Fatalf("unrelated native reminder was cleared, changed or rebuilt: all=%d queued=%d notice=%+v", port.allCalls, port.queued, port.pending[unrelatedID])
	}
	log, err := store.Tombstones(ctx)
	if err != nil || len(log) != 1 || log[0].SourceID != row.SourceID {
		t.Fatalf("meeting source deletion was not recorded for sync: %+v, %v", log, err)
	}
}
