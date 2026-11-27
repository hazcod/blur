package ingest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/platform"
)

func captureOnce(t *testing.T, app, text string) (*Service, *activity.Engine, capture.SanitizedObservation, func(string) bool) {
	t.Helper()
	store := openTestStore(t)
	o, err := capture.SanitizeObservation(app, "", text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	source := &eventSource{probe: platform.CaptureProbe{TargetID: "opaque", AppID: app}, events: make(chan platform.CaptureEvent)}
	engine := activity.New(activity.Config{MaxGap: time.Hour, RingSize: 16})
	service, err := newServiceWithActivity(source, captureFunc(func(context.Context) (capture.SanitizedObservation, error) { return o, nil }), store, engine, store, time.Now().Add(-time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.captureCurrent(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored := func(id string) bool {
		_, found, err := store.GetObservation(context.Background(), id)
		return err == nil && found
	}
	return service, engine, o, stored
}

func TestMinimumContentBoundary(t *testing.T) {
	for _, c := range []struct {
		name, app, text string
		stored          bool
	}{
		{"single terminal code point stored", "com.apple.terminal", "0", true},
		{"short command stored", "com.apple.terminal", "ls", true},
		{"single Unicode code point stored", "org.example.editor", "日", true},
		{"31 chars stored", "org.example.editor", strings.Repeat("x", 31), true},
		{"32 chars stored", "org.example.editor", strings.Repeat("x", 32), true},
		{"padded single code point stored", "org.example.editor", "  x  \n\t ", true},
		{"short voice transcript", "sh.blur.meetings", "hi", true},
		{"short typed note", "sh.blur.notes", "hi", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			service, _, o, stored := captureOnce(t, c.app, c.text)
			stats := service.Stats()
			if stored(o.ContentID()) != c.stored {
				t.Fatalf("stored should be %t", c.stored)
			}
			if c.stored && (stats.Persisted != 1 || stats.DroppedShort != 0) {
				t.Fatalf("stats = %+v", stats)
			}
			if !c.stored && (stats.Persisted != 0 || stats.DroppedShort != 1) {
				t.Fatalf("stats = %+v", stats)
			}
		})
	}
}

func TestShortCaptureLinksForegroundVisitToSavedEvidence(t *testing.T) {
	service, engine, observation, stored := captureOnce(t, "org.example.editor", "tiny")
	time.Sleep(2 * time.Millisecond)
	if err := service.captureCurrent(context.Background()); err != nil {
		t.Fatal(err)
	}
	segments := engine.Segments()
	if !stored(observation.ContentID()) || len(segments) != 1 || segments[0].AppID != "org.example.editor" || len(segments[0].SourceIDs) != 1 || segments[0].SourceIDs[0] != observation.ContentID() {
		t.Fatalf("short visit lost saved evidence or its activity link: %#v", segments)
	}
}

func TestWhitespaceOnlyCaptureIsNotCountedAsDroppedShort(t *testing.T) {
	service, _, _, _ := captureOnce(t, "org.example.editor", "   \n ")
	if stats := service.Stats(); stats.DroppedShort != 0 || stats.Skipped == 0 {
		t.Fatalf("stats = %+v", stats)
	}
}
