package deletion

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

type capturePartNotificationGuard struct {
	held      bool
	cancelled map[string]bool
}

func (g *capturePartNotificationGuard) GuardSources(context.Context, []string) (func(), error) {
	return nil, errors.New("must prepare all sibling source identities")
}
func (g *capturePartNotificationGuard) GuardAll(context.Context) (func(), error) {
	return nil, errors.New("must preserve unrelated notifications")
}
func (g *capturePartNotificationGuard) WithSourceDeletionGuard(ctx context.Context, run func(context.Context, func([]tasks.Item) error) error) error {
	g.held = true
	defer func() { g.held = false }()
	return run(ctx, func(items []tasks.Item) error {
		if !g.held {
			return errors.New("native scheduling gate released")
		}
		for _, item := range items {
			g.cancelled[item.ID] = true
		}
		return nil
	})
}

func TestDeleteCapturePartPreparesSiblingNotificationsAndVolatileActivity(t *testing.T) {
	for _, mode := range []string{"direct", "cutoff", "newer-preserved"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			at := time.Now().UTC().Add(-time.Hour)
			id := publishDeletionCapture(t, store, strings.Repeat("A complete captured technical paragraph.\n", 1900), at)
			for range 64 {
				more, err := store.IndexPendingCaptures(ctx, 16)
				if err != nil {
					t.Fatal(err)
				}
				if !more {
					break
				}
			}
			ids, more, err := store.ListCaptureSourceIDs(ctx, id, -1, 64)
			if err != nil || more || len(ids) < 2 {
				t.Fatal("multipart fixture", len(ids), more, err)
			}
			engine := activity.New(activity.Config{})
			for i, sourceID := range ids {
				item := tasks.Item{ID: fmt.Sprintf("%032x", i+1), Revision: 1, Title: "Review captured source", State: tasks.Open, CreatedAt: at, UpdatedAt: at, SourceID: sourceID, Reminder: &tasks.Reminder{DueAt: at.Add(2 * time.Hour), Timezone: "UTC"}, Delivery: tasks.Delivery{State: tasks.NeedsSync, At: at}}
				if err = store.PutTask(ctx, item, 0); err != nil {
					t.Fatal("part-derived task admission", err)
				}
				if _, err = engine.Apply(activity.Event{Kind: activity.Seen, Monotonic: int64(i) * int64(time.Second), Wall: at.Add(time.Duration(i) * time.Second), SourceID: sourceID, AppID: "com.example.reader", Domain: "example.test"}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = engine.Apply(activity.Event{Kind: activity.Idle, Monotonic: int64(len(ids)) * int64(time.Second), Wall: at.Add(time.Duration(len(ids)) * time.Second)}); err != nil {
				t.Fatal(err)
			}
			guard := &capturePartNotificationGuard{cancelled: map[string]bool{}}
			service, err := NewService(store, engine)
			if err != nil {
				t.Fatal(err)
			}
			service.SetNotificationCleanup(guard)
			if mode == "direct" {
				_, err = service.DeleteSource(ctx, ids[len(ids)-1])
			} else {
				cutoff := at.Add(time.Second)
				if mode == "newer-preserved" {
					cutoff = at.Add(-time.Second)
				}
				_, err = service.DeleteSourceThrough(ctx, ids[len(ids)-1], cutoff)
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "newer-preserved" {
				if len(guard.cancelled) != 0 || len(engine.Segments()) == 0 {
					t.Fatal("newer capture lost reminders or live activity")
				}
				return
			}
			if len(guard.cancelled) != len(ids) {
				t.Fatal("sibling notices survived parent deletion", len(guard.cancelled), len(ids))
			}
			if len(engine.Segments()) != 0 {
				t.Fatal("sibling capture remained in volatile activity")
			}
			for i := range ids {
				if _, found, err := store.GetTask(ctx, fmt.Sprintf("%032x", i+1)); err != nil || found {
					t.Fatal("part task survived", found, err)
				}
			}
		})
	}
}
