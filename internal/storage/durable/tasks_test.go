package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/tasks"
)

func TestTaskMultiSourceProvenanceCascadesFromEverySource(t *testing.T) {
	for _, removed := range []int{0, 1} {
		t.Run(fmt.Sprint(removed), func(t *testing.T) {
			ctx := context.Background()
			s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe1), CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func(close func() error) { _ = close() }(s.Close)
			var ids []string
			for _, text := range []string{"Alice requested a review", "The review belongs to Atlas"} {
				source, err := capture.SanitizeObservation("com.example.chat", "local", text, 4096)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.PutObservationAt(ctx, source, time.Now()); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, source.ContentID())
			}
			sort.Strings(ids)
			item := taskFixture(1)
			item.UserAuthored, item.SourceID, item.SourceIDs = false, ids[0], ids
			if err := s.PutTask(ctx, item, 0); err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				linked, err := s.TasksForSources(ctx, []string{id})
				if err != nil || len(linked) != 1 || linked[0].ID != item.ID {
					t.Fatal("a task lost one of its evidence links", linked, err)
				}
			}
			if err := s.DeleteObservationRecord(ctx, ids[removed]); err != nil {
				t.Fatal(err)
			}
			if _, found, err := s.GetTask(ctx, item.ID); err != nil || found {
				t.Fatal("deleted-source task remained", found, err)
			}
			if linked, err := s.TasksForSources(ctx, ids); err != nil || len(linked) != 0 {
				t.Fatal("stale reverse provenance remained", linked, err)
			}
			if err := s.PutTask(ctx, item, 0); !errors.Is(err, tasks.ErrSourceGone) {
				t.Fatal("a late job restored a task linked to a deleted source", err)
			}
		})
	}
}

func TestTaskSourceLinksCannotBeRewrittenByAnEdit(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe2), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	var ids []string
	for _, text := range []string{"First source", "Second source"} {
		source, _ := capture.SanitizeObservation("com.example.chat", "local", text, 4096)
		if err := s.PutObservationAt(ctx, source, time.Now()); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, source.ContentID())
	}
	sort.Strings(ids)
	item := taskFixture(1)
	item.UserAuthored, item.SourceID, item.SourceIDs = false, ids[0], ids
	if err := s.PutTask(ctx, item, 0); err != nil {
		t.Fatal(err)
	}
	item.Revision++
	item.SourceIDs = nil
	if err := s.PutTask(ctx, item, 1); !errors.Is(err, tasks.ErrConflict) {
		t.Fatal("editing discarded source provenance", err)
	}
}

func TestTaskMultiSourceFanoutDeletionClearsEveryReverseLink(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe6), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	var ids []string
	for n := 0; n < tasks.MaxSources; n++ {
		source, _ := capture.SanitizeObservation("com.example.chat", "local", fmt.Sprintf("Review context %d", n), 4096)
		if err := s.PutObservationAt(ctx, source, time.Now()); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, source.ContentID())
	}
	sort.Strings(ids)
	for n := 1; n <= tasks.MaxSourceTasks; n++ {
		item := taskFixture(n)
		item.UserAuthored, item.SourceID, item.SourceIDs = false, ids[0], ids
		if err := s.PutTask(ctx, item, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteObservationRecord(ctx, ids[len(ids)-1]); err != nil {
		t.Fatal("bounded multi-source task fanout exceeded deletion capacity", err)
	}
	page, err := s.ListTasks(ctx, tasks.PageRequest{Limit: 32})
	if err != nil || len(page.Items) != 0 || page.HasMore {
		t.Fatal("source deletion retained tasks", page, err)
	}
	if linked, err := s.TasksForSources(ctx, ids); err != nil || len(linked) != 0 {
		t.Fatal("multi-task deletion corrupted another source's reverse directory", linked, err)
	}
}

func taskFixture(number int) tasks.Item {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	return tasks.Item{ID: fmt.Sprintf("%032x", number), Revision: 1, Title: "Private task title", State: tasks.Open, CreatedAt: at, UpdatedAt: at, UserAuthored: true, Reminder: &tasks.Reminder{DueAt: at.Add(time.Hour), Timezone: "Europe/Brussels"}, Delivery: tasks.Delivery{State: tasks.NeedsSync, At: at}}
}

func TestTaskUniqueSecondarySourceFanoutDeletion(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe8), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	primary, _ := capture.SanitizeObservation("chat.app", "local", "Shared primary evidence", 4096)
	if err := s.PutObservation(ctx, primary); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= tasks.MaxSourceTasks; n++ {
		ids := []string{primary.ContentID()}
		for k := 1; k < tasks.MaxSources; k++ {
			o, _ := capture.SanitizeObservation("chat.app", "local", fmt.Sprintf("Context task %d source %d", n, k), 4096)
			if err := s.PutObservation(ctx, o); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, o.ContentID())
		}
		sort.Strings(ids)
		item := taskFixture(n)
		item.UserAuthored, item.SourceID, item.SourceIDs = false, ids[0], ids
		if err := s.PutTask(ctx, item, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteObservationRecord(ctx, primary.ContentID()); err != nil {
		t.Fatal("distinct secondary directories prevented source deletion", err)
	}
	page, err := s.ListTasks(ctx, tasks.PageRequest{Limit: 32})
	if err != nil || len(page.Items) != 0 || page.HasMore {
		t.Fatal("source deletion retained tasks", page, err)
	}
}

func TestTasksEncryptedReopenCASAndCompleteDeletion(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xd1)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	item := taskFixture(1)
	if err := s.PutTask(ctx, item, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(ctx, item, 0); !errors.Is(err, tasks.ErrConflict) {
		t.Fatalf("duplicate create replaced task: %v", err)
	}
	if err := s.SetTaskDelivery(ctx, item.ID, item.Revision, tasks.Delivery{State: tasks.Scheduled, At: item.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{item.ID, item.Title, "Europe/Brussels"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("task persisted in plaintext")
		}
	}
	clear(raw)
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetTask(ctx, item.ID)
	if err != nil || !found || got.Delivery.State != tasks.Scheduled {
		t.Fatalf("reopen: %+v %v", got, err)
	}
	got.Revision++
	got.State = tasks.Completed
	got.UpdatedAt = got.UpdatedAt.Add(time.Minute)
	if err := s.PutTask(ctx, got, item.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskDelivery(ctx, item.ID, item.Revision, tasks.Delivery{State: tasks.Delivered, At: got.UpdatedAt}); !errors.Is(err, tasks.ErrConflict) {
		t.Fatalf("late delivery modified completed revision: %v", err)
	}
	if err := s.DeleteAllTasks(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListTasks(ctx, tasks.PageRequest{Limit: 32})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("delete all tasks: %+v %v", page, err)
	}
}

func TestTasksSourceProvenanceCascadeAndLateJob(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xd2), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	source, err := capture.SanitizeObservation("sh.blur.notes", "local", "Task: review compiler release", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(ctx, source, time.Now()); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= tasks.MaxSourceTasks; n++ {
		item := taskFixture(n)
		item.UserAuthored = false
		item.SourceID = source.ContentID()
		if err := s.PutTask(ctx, item, 0); err != nil {
			t.Fatal(n, err)
		}
	}
	linked, err := s.TasksForSources(ctx, []string{source.ContentID()})
	if err != nil || len(linked) != tasks.MaxSourceTasks {
		t.Fatal(len(linked), err)
	}
	if err := s.DeleteObservationRecord(ctx, source.ContentID()); err != nil {
		t.Fatal("full task fanout deletion failed", err)
	}
	page, err := s.ListTasks(ctx, tasks.PageRequest{Limit: 32})
	if err != nil || len(page.Items) != 0 || page.HasMore {
		t.Fatal("source tasks remained", page, err)
	}
	late := taskFixture(70)
	late.UserAuthored = false
	late.SourceID = source.ContentID()
	if err := s.PutTask(ctx, late, 0); !errors.Is(err, tasks.ErrSourceGone) {
		t.Fatalf("late job restored deleted source task: %v", err)
	}
}

func TestTaskPagingIsStableAndIndependentOfLibraryAndSemanticDirectories(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xd3), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	for i := 1; i <= 70; i++ {
		if err := s.PutTask(ctx, taskFixture(i), 0); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	after := ""
	for i := 0; i < 10; i++ {
		page, err := s.ListTasks(ctx, tasks.PageRequest{Limit: 13, After: after})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatal("duplicate task page")
			}
			seen[item.ID] = true
		}
		if !page.HasMore {
			break
		}
		if page.Next == after || page.Next == "" {
			t.Fatal("cursor did not advance")
		}
		after = page.Next
	}
	if len(seen) != 70 {
		t.Fatal("paging omitted tasks", len(seen))
	}
}
