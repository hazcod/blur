package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/storage"
)

func TestUnchangedCapturePersistsCurrentVisitBeforeActivityCloses(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	o, _ := capture.SanitizeObservation("editor.app", "", "Mira is the rollback owner. plus enough extra visible detail", 512)
	if err := store.PutObservationAt(ctx, o, time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	source := &eventSource{probe: platform.CaptureProbe{TargetID: "opaque", AppID: o.AppID()}, events: make(chan platform.CaptureEvent)}
	service, err := newService(source, captureFunc(func(context.Context) (capture.SanitizedObservation, error) { return o, nil }), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	changed, enriched := 0, 0
	service.onChange = func() { changed++ }
	service.onObservation = func(capture.SanitizedObservation, time.Time) { enriched++ }
	before := time.Now().Add(-time.Second)
	for i := 0; i < 2; i++ {
		if err := service.captureCurrent(ctx); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.ListObservationWindow(ctx, storage.ObservationWindowRequest{Start: before, End: time.Now().Add(time.Second), Limit: 5, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 1 {
		t.Fatalf("active unchanged page invisible today: rows=%d err=%v", len(page.Observations), err)
	}
	if changed != 2 || enriched != 0 {
		t.Fatalf("visits must refresh readers and reuse primary enrichment: changed=%d enriched=%d", changed, enriched)
	}
}
