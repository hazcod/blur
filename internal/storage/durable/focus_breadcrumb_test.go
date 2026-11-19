package durable

import (
	"context"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
)

func TestFocusBreadcrumbRoundTripsThroughDurableStore(t *testing.T) {
	ctx := context.Background()
	path := durableTestPath(t)
	s, err := OpenDurable(path, durableTestKey(t, 0x9a), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	plain, err := capture.SanitizeObservation("editor.app", "", "A document body long enough to index.", 512)
	if err != nil {
		t.Fatal(err)
	}
	withFocus := plain.WithFocus("AXTextField", "Subject", "Compose > Header")
	legacy, err := capture.SanitizeObservation("legacy.app", "", "A record written before breadcrumbs existed.", 512)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	if err := s.PutObservationAt(ctx, withFocus, at); err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(ctx, legacy, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	evidence, found, err := storage.ReadSourceEvidence(ctx, s, withFocus.ContentID())
	if err != nil || !found {
		t.Fatalf("read: found=%v err=%v", found, err)
	}
	if evidence.FocusedRole() != "AXTextField" || evidence.FocusedName() != "Subject" || evidence.FocusedPath() != "Compose > Header" {
		t.Fatalf("breadcrumb lost: %q %q %q", evidence.FocusedRole(), evidence.FocusedName(), evidence.FocusedPath())
	}
	old, found, err := storage.ReadSourceEvidence(ctx, s, legacy.ContentID())
	if err != nil || !found || old.FocusedRole() != "" || old.FocusedName() != "" || old.FocusedPath() != "" {
		t.Fatalf("legacy record: found=%v err=%v %+v", found, err, old)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDurable(path, durableTestKey(t, 0x9a), OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(reopened.Close)
	again, found, err := storage.ReadSourceEvidence(ctx, reopened, withFocus.ContentID())
	if err != nil || !found || again.FocusedName() != "Subject" {
		t.Fatalf("after reopen: found=%v err=%v %q", found, err, again.FocusedName())
	}
}
