package deletion

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func publishDeletionCapture(t *testing.T, s *durable.Store, text string, at time.Time) string {
	t.Helper()
	ctx := context.Background()
	b, err := capture.NewCaptureBuilder("com.example.reader", "example.test", strings.Repeat("d", 64), at)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Abort()
	if err := s.BeginCapture(ctx, b.Header()); err != nil {
		t.Fatal(err)
	}
	sink := func(ctx context.Context, parts []capture.CapturePart) error {
		return s.AppendCaptureParts(ctx, b.Header(), parts)
	}
	for len(text) > 0 {
		n := min(len(text), 64000)
		if err := b.Write(ctx, text[:n], sink); err != nil {
			t.Fatal(err)
		}
		text = text[n:]
	}
	summary, err := b.Finish(ctx, capture.CaptureComplete, sink)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	return summary.ContentID()
}

func TestDeleteAllIncludesCapturesOutsideObservationCatalog(t *testing.T) {
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	// Future fixture time prevents the all-source fence from hiding a payload
	// that the coordinator forgot to erase. This checks actual catalog removal.
	id := publishDeletionCapture(t, s, strings.Repeat("Complete document evidence. ", 42000), time.Now().UTC().Add(time.Hour))
	service, err := NewService(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := service.DeleteAll(ctx)
	if err != nil || n != 1 {
		t.Fatal("delete-all omitted capture", n, err)
	}
	if _, found, err := s.GetCapture(ctx, id); err != nil || found {
		t.Fatal("delete-all retained capture payload", found, err)
	}
}

func TestDeleteBeforeIncludesCaptureRetentionAndPreservesBoundary(t *testing.T) {
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	cutoff := time.Now().UTC().Add(-time.Hour)
	expired := publishDeletionCapture(t, s, "Expired complete document", cutoff.Add(-time.Second))
	boundary := publishDeletionCapture(t, s, "Boundary complete document", cutoff)
	service, err := NewService(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := service.DeleteBefore(ctx, cutoff)
	if err != nil || n != 1 {
		t.Fatal("retention omitted capture", n, err)
	}
	if _, found, err := s.GetCapture(ctx, expired); err != nil || found {
		t.Fatal("retention retained expired capture", found, err)
	}
	if _, found, err := s.GetCapture(ctx, boundary); err != nil || !found {
		t.Fatal("retention erased boundary capture", found, err)
	}
}
