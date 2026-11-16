package client

import (
	"context"
	"testing"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/ingest"
	"github.com/hazcod/blur/internal/platform"
)

func TestDeletingTypedNoteKeepsCaptureRunning(t *testing.T) {
	source, err := capture.SanitizeObservation("sh.blur.notes", "local", "# Typed note\n\nKeep capture running.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	controller := ingest.NewController(&startupCaptureSource{state: platform.Available}, func(context.Context) (func(context.Context) error, error) {
		return func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, nil
	})
	if err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Pause(context.Background()) })
	deleter := &testDeleter{}
	client := NewClient(&testData{observations: map[string]capture.SanitizedObservation{source.ContentID(): source}}).WithDeletion(deleter)
	app := &meetingOperationTestApp{posted: make(chan func(), 8)}
	d := &desktop{client: client, app: app, capture: controller, page: "Settings", pendingDelete: source.ContentID()}
	d.preferences.CaptureEnabled = true
	d.deleteSource(source.ContentID())
	app.finish(t)
	if deleter.calls != 1 || deleter.id != source.ContentID() {
		t.Fatal("typed note was not deleted", d.status)
	}
	if !controller.Running() || !d.preferences.CaptureEnabled {
		t.Fatal("deleting a typed note paused passive capture")
	}
}
