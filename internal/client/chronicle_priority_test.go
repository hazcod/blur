package client

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestProductionChronicleRepositoryForwardsExactSourceClockAndFencesMemory(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	c := NewClient(store)
	defer c.Close()
	source, err := capture.SanitizeObservation("editor.app", "", "Synthetic recap source", 1024)
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 11, 13, 10, 1, 0, 0, time.UTC)
	if err := store.PutObservationAt(ctx, source, first); err != nil {
		t.Fatal(err)
	}
	verified := sourceVerifiedRepository{inner: store, sources: store, client: c}
	var repository chronicle.SummaryRepository = verified
	reader, ok := repository.(interface {
		GetObservationWithCaptureTime(context.Context, string) (capture.SanitizedObservation, time.Time, bool, error)
	})
	if !ok {
		t.Fatal("production wrapper hides immutable source reader from retry worker")
	}
	got, at, found, err := reader.GetObservationWithCaptureTime(ctx, source.ContentID())
	if err != nil || !found || got.ContentID() != source.ContentID() || !at.Equal(first) {
		t.Fatal("wrapper lost exact saved-first source clock", err)
	}
	c.NotifyMemoryChanging()
	if _, _, _, err := reader.GetObservationWithCaptureTime(ctx, source.ContentID()); !errors.Is(err, ErrMemoryChanging) {
		t.Fatal("source preflight crossed active deletion window", err)
	}
	c.NotifyMemoryStable()
	if err := store.DeleteObservationRecord(ctx, source.ContentID()); err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := reader.GetObservationWithCaptureTime(ctx, source.ContentID()); err != nil || found {
		t.Fatal("wrapper source preflight resurrected erased evidence", err)
	}
}
