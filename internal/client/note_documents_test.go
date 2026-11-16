package client

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

type noteDocumentNotices struct {
	clientNoticePort
	removeError error
}

func (p *noteDocumentNotices) Remove(ctx context.Context, ids []string) error {
	if p.removeError != nil {
		return p.removeError
	}
	return p.clientNoticePort.Remove(ctx, ids)
}

func TestNoteDocumentClientEditKeepsNativeReminderAndOldSourceDeleteErasesAllRevisions(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	c := NewClient(store).WithNotes(notes.NewService(store, nil, nil))
	defer c.Close()
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.WithDeletion(deleter)
	now := time.Now()
	port := &noteDocumentNotices{}
	service, err := reminders.NewService(store, port, reminders.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	deleter.SetNotificationCleanup(service)
	first, err := c.CreateNoteDocument(ctx, "# My note\n\n- [ ] Review the draft\n")
	if err != nil {
		t.Fatal(err)
	}
	linked, err := service.Create(ctx, "Review the draft", &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "UTC"}, first.SourceID, false)
	if err != nil || linked.ScheduleError != nil {
		t.Fatal(linked, err)
	}
	if len(port.pending) != 1 {
		t.Fatal("native reminder did not schedule", port.pending)
	}
	second, err := c.UpdateNoteDocument(ctx, first.ID, first.Revision, "# My edited note\n\n**Markdown** stays editable.\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(port.pending) != 1 {
		t.Fatal("editing removed native reminder", port.pending)
	}
	item, found, err := store.GetTask(ctx, linked.Item.ID)
	if err != nil || !found || item.SourceID != first.SourceID || item.Revision != linked.Item.Revision {
		t.Fatal("editing altered confirmed task", item, found, err)
	}
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		t.Fatal("edit left mutation window open")
	}
	if _, err := c.UpdateNoteDocument(ctx, first.ID, first.Revision, "conflicting draft"); !errors.Is(err, notes.ErrDocumentConflict) {
		t.Fatalf("stale editor did not conflict: %v", err)
	}
	if current, _ := c.BeginMemoryRead(); current != epoch {
		t.Fatal("conflict invalidated unsaved editor draft")
	}
	got, err := c.NoteDocument(ctx, first.SourceID)
	if err != nil || got.SourceID != second.SourceID || got.Text != "# My edited note\n\n**Markdown** stays editable.\n" {
		t.Fatal("stable document read/export", got, err)
	}
	port.removeError = errors.New("native removal failed")
	if err := c.DeleteNoteDocument(ctx, first.ID, second.Revision); !errors.Is(err, port.removeError) {
		t.Fatal("deletion ignored native cleanup failure", err)
	}
	if _, found, err := store.GetObservation(ctx, first.SourceID); err != nil || !found {
		t.Fatal("failed native removal erased proof", found, err)
	}
	port.removeError = nil
	if _, err := c.DeleteSource(first.SourceID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.SourceID, second.SourceID} {
		if _, found, err := store.GetObservation(ctx, id); err != nil || found {
			t.Fatal("generic source delete left a revision", found, err)
		}
	}
	if len(port.pending) != 0 {
		t.Fatal("note deletion left native reminder", port.pending)
	}
}
