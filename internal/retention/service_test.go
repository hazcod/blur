package retention

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestRunIfDuePrunesOnlyExpiredRecordsAndRunsAtMostDaily(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, deleter)
	if err != nil {
		t.Fatal(err)
	}
	if policy, err := service.Policy(ctx); err != nil || policy.Days != 90 || !policy.LastSweepAt.IsZero() {
		t.Fatalf("fresh retention policy = %+v, %v; want 90 days", policy, err)
	}
	if removed, ran, err := service.RunIfDue(ctx, time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)); err != nil || !ran || removed != 0 {
		t.Fatalf("empty default sweep = (%d, %t, %v), want successful no-deletion sweep", removed, ran, err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	old, err := capture.SanitizeObservation("editor", "", "older than policy", 128)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := capture.SanitizeObservation("editor", "", "within policy", 128)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		observation capture.SanitizedObservation
		at          time.Time
	}{{old, now.Add(-31 * 24 * time.Hour)}, {newer, now.Add(-29 * 24 * time.Hour)}} {
		if err := store.PutObservationAt(ctx, row.observation, row.at); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.ConfigureDays(ctx, 30); err != nil {
		t.Fatal(err)
	}
	removed, ran, err := service.RunIfDue(ctx, now)
	if err != nil || !ran || removed != 1 {
		t.Fatalf("configured sweep = (%d, %t, %v), want one removal and run", removed, ran, err)
	}
	if _, found, err := store.GetObservation(ctx, old.ContentID()); err != nil || found {
		t.Fatalf("expired source remains: found=%t err=%v", found, err)
	}
	if _, found, err := store.GetObservation(ctx, newer.ContentID()); err != nil || !found {
		t.Fatalf("recent source was deleted: found=%t err=%v", found, err)
	}
	removed, ran, err = service.RunIfDue(ctx, now.Add(23*time.Hour))
	if err != nil || ran || removed != 0 {
		t.Fatalf("second same-day sweep = (%d, %t, %v), want no-op", removed, ran, err)
	}
	policy, err := store.GetRetentionPolicy(ctx)
	if err != nil || policy.Days != 30 || !policy.LastSweepAt.Equal(now) {
		t.Fatalf("retention policy after sweep = %+v, %v", policy, err)
	}
}

func TestRunIfDueFailsClosedOnUntrustworthyClock(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, deleter)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConfigureDays(context.Background(), 30); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RunIfDue(context.Background(), time.Time{}); err == nil {
		t.Fatal("RunIfDue(zero time) succeeded")
	}
}

func TestBeforeSweepRunsOnlyWhenExpiredMemoryWillBePruned(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, deleter)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConfigureDays(ctx, 0); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	old, err := capture.SanitizeObservation("editor", "", "expired in retention test", 128)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, old, now.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	called, completed, foundBefore, foundAfter := 0, 0, false, true
	service.SetBeforeSweep(func() {
		called++
		_, foundBefore, _ = store.GetObservation(ctx, old.ContentID())
	})
	service.SetAfterSweep(func() {
		completed++
		_, foundAfter, _ = store.GetObservation(ctx, old.ContentID())
	})
	if _, ran, err := service.RunIfDue(ctx, now); err != nil || ran || called != 0 || completed != 0 {
		t.Fatalf("disabled retention called hook: ran=%t before=%d after=%d err=%v", ran, called, completed, err)
	}
	if err := service.ConfigureDays(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := service.RunIfDue(ctx, now); err != nil || !ran || called != 1 || completed != 1 || !foundBefore || foundAfter {
		t.Fatalf("due retention hook state: ran=%t before=%d after=%d existed-before=%t existed-after=%t err=%v", ran, called, completed, foundBefore, foundAfter, err)
	}
}

func TestDefaultNinetyDayRetentionPrunesExpiredSourceWithoutSetup(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, deleter)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	old, err := capture.SanitizeObservation("editor", "", "older than default retention", 128)
	if err != nil {
		t.Fatal(err)
	}
	recent, err := capture.SanitizeObservation("editor", "", "within default retention", 128)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, old, now.Add(-91*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, recent, now.Add(-89*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	removed, ran, err := service.RunIfDue(ctx, now)
	if err != nil || !ran || removed != 1 {
		t.Fatalf("default retention sweep = (%d, %t, %v), want one expired source removed", removed, ran, err)
	}
	if _, found, err := store.GetObservation(ctx, old.ContentID()); err != nil || found {
		t.Fatalf("expired source remained: found=%t err=%v", found, err)
	}
	if _, found, err := store.GetObservation(ctx, recent.ContentID()); err != nil || !found {
		t.Fatalf("unexpired source was removed: found=%t err=%v", found, err)
	}
}

func TestRunNowCanApplyAnExplicitPolicyChangeImmediately(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, deleter)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := service.ConfigureDays(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRetentionSweep(ctx, now); err != nil {
		t.Fatal(err)
	}
	old, err := capture.SanitizeObservation("editor", "", "newly expired by user choice", 128)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, old, now.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := service.RunIfDue(ctx, now.Add(time.Hour)); err != nil || ran {
		t.Fatalf("periodic sweep unexpectedly ran early: ran=%t err=%v", ran, err)
	}
	removed, err := service.RunNow(ctx, now.Add(time.Hour))
	if err != nil || removed != 1 {
		t.Fatalf("RunNow() = (%d, %v), want one immediate deletion", removed, err)
	}
}

func TestSweepUsesBoundedPagesForMoreThanOnePage(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, deleter)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := service.ConfigureDays(ctx, 30); err != nil {
		t.Fatal(err)
	}
	for i := range storage.MaxObservationPageRows + 2 {
		observation, err := capture.SanitizeObservation("editor", "", fmt.Sprintf("old retention source %03d", i), 128)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, observation, now.Add(-31*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	removed, ran, err := service.RunIfDue(ctx, now)
	if err != nil || !ran || removed != storage.MaxObservationPageRows+2 {
		t.Fatalf("multi-page sweep = (%d, %t, %v), want %d removals", removed, ran, err, storage.MaxObservationPageRows+2)
	}
}
