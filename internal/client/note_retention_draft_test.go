package client

import (
	"context"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/retention"
)

func TestEmptyRetentionSweepPreservesNewUnsavedNoteDraft(t *testing.T) {
	ctx := context.Background()
	c, store, _ := todayTaskClient(t)
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := retention.NewService(store, deleter)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfigureDays(ctx, 30); err != nil {
		t.Fatal(err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 64)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	const draft = "Several hours of new, unsaved writing"
	d.noteUI.open, d.noteUI.draft = true, draft
	c.SetMemoryInvalidator(d.invalidateMemoryViews)
	svc.SetBeforeSweep(c.NotifyMemoryChanging)
	svc.SetAfterSweep(c.NotifyMemoryStable)
	removed, err := svc.RunNow(ctx, time.Now())
	if err != nil || removed != 0 {
		t.Fatal("fixture unexpectedly removed memory", removed, err)
	}
	if !d.noteUI.open || d.noteUI.draft != draft {
		t.Fatal("empty retention sweep discarded new authored writing")
	}
	// Explicit navigation/discard still clears the draft.
	d.invalidateNoteDocumentView()
	if d.noteUI.open || d.noteUI.draft != "" {
		t.Fatal("explicit discard retained draft")
	}
}
