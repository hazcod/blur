package durable

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
)

// This reproduces the transport boundary against a real encrypted temporary
// store: a published multipart capture is readable locally but absent from the
// only replica export API.
func TestReplicaObservationTransportOmitsPublishedMultipartCapture(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x71), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	at := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	header, parts, summary := makeStoredCapture(t, "terminal status: agent deploy complete", at)
	stageStoredCapture(t, store, header, parts)
	if _, err := store.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	if got, found, err := store.GetCapture(ctx, summary.ContentID()); err != nil || !found || got.Summary.ContentID() != summary.ContentID() {
		t.Fatalf("published source is not locally readable: found=%v err=%v", found, err)
	}
	page, err := store.ExportReplicaObservations(ctx, "00000000-0000-4000-8000-000000000001", nil, 16, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer clearExportPage(&page)
	if len(page.Items) != 0 {
		t.Fatalf("observation-only transport unexpectedly returned capture data: %d items", len(page.Items))
	}
}

func TestReplicaCapturePartsPreservePublicationIdentityAndReplay(t *testing.T) {
	ctx := context.Background()
	source, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x72), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x73), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()

	at := time.Date(2026, 11, 22, 11, 0, 0, 0, time.UTC)
	header, parts, summary := makeStoredCapture(t, strings.Repeat("agent deploy completed. ", 5000), at)
	stageStoredCapture(t, source, header, parts)
	publication := strings.Repeat("a", 64)
	if _, err := source.PublishCaptureWithPublication(ctx, summary, publication); err != nil {
		t.Fatal(err)
	}
	for _, part := range parts {
		if err := target.ApplyReplicaCapturePart(ctx, summary, publication, part); err != nil {
			t.Fatalf("apply part %d: %v", part.Index(), err)
		}
	}
	for _, part := range parts {
		if err := target.ApplyReplicaCapturePart(ctx, summary, publication, part); err != nil {
			t.Fatalf("replay part %d: %v", part.Index(), err)
		}
	}
	got, found, err := target.GetCapture(ctx, summary.ContentID())
	if err != nil || !found || got.Publication != publication {
		t.Fatalf("imported publication identity = %q, found=%v err=%v", got.Publication, found, err)
	}
	if gotSourceID := capture.CapturePartSourceID(summary.ContentID(), got.Publication, parts[0].Index()); gotSourceID != capture.CapturePartSourceID(summary.ContentID(), publication, 0) {
		t.Fatalf("imported source identity changed: %s", gotSourceID)
	}
	if err := target.ApplyReplicaCapturePart(ctx, summary, strings.Repeat("b", 64), parts[0]); err == nil {
		t.Fatal("conflicting publication token was accepted")
	}
}
