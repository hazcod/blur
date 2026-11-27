package durable

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/tasks"
)

func cleanupTask(t *testing.T, s *Store, number int, source string) tasks.Item {
	t.Helper()
	i := taskFixture(number)
	i.Reminder = &tasks.Reminder{DueAt: i.CreatedAt.Add(time.Hour), Timezone: "UTC"}
	i.Delivery = tasks.Delivery{State: tasks.Scheduled, At: i.CreatedAt, Attempted: true, NextAt: i.Reminder.DueAt}
	if source != "" {
		i.SourceID, i.UserAuthored = source, false
	}
	if err := s.PutTask(context.Background(), i, 0); err != nil {
		t.Fatal(err)
	}
	saved, found, err := s.GetTask(context.Background(), i.ID)
	if err != nil || !found {
		t.Fatal("missing saved cleanup task", found, err)
	}
	return saved
}

func TestReminderCleanupPreservesUnrelatedTasksDuringInterruptedErasureAndReopen(t *testing.T) {
	for _, phase := range []int{14, 0, 1, 10} {
		t.Run(fmt.Sprint(phase), func(t *testing.T) {
			ctx := context.Background()
			s, path, key := meetingStore(t)
			m := fixtureMeeting("cleanup-recovery")
			if err := s.PutMeeting(ctx, m, 0); err != nil {
				t.Fatal(err)
			}
			r, err := s.Append(ctx, fixtureMeetingFinal(m.ID, "one", "Private source to erase"))
			if err != nil {
				t.Fatal(err)
			}
			linked := cleanupTask(t, s, 1, r.SourceID)
			user := cleanupTask(t, s, 2, "")
			o, _ := capture.SanitizeObservation("editor.app", "unrelated.example", "Unrelated task source", 4096)
			if err := s.PutObservationAt(ctx, o, m.StartedAt); err != nil {
				t.Fatal(err)
			}
			other := cleanupTask(t, s, 3, o.ContentID())
			ids := []string{reminders.NotificationID(user), reminders.NotificationID(linked), reminders.NotificationID(other)}
			interrupted := errors.New("stop after committed erasure step")
			err = s.deleteMeetingWithHook(ctx, m.ID, func() error {
				journal, found, err := s.readSourceErasureLocked(ctx)
				if err != nil {
					return err
				}
				if found && journal.Phase == phase {
					return interrupted
				}
				return nil
			})
			if !errors.Is(err, interrupted) {
				t.Fatal("fixture did not interrupt target phase", phase, err)
			}
			if _, _, err := s.GetTask(ctx, user.ID); !errors.Is(err, ErrDeletionPending) {
				t.Fatal("ordinary read bypassed pending erasure", err)
			}
			for _, reopened := range []bool{false, true} {
				if reopened {
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					s, err = OpenDurable(path, key, OpenExisting)
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
				}
				got, err := s.CleanupReminderIDs(ctx, m.ID, ids)
				if err != nil || !slices.Equal(got, []string{reminders.NotificationID(linked)}) {
					t.Fatalf("cleanup exposed unrelated native IDs: phase=%d reopened=%t got=%v err=%v", phase, reopened, got, err)
				}
				if !reopened {
					if _, _, err := s.GetTask(ctx, user.ID); !errors.Is(err, ErrDeletionPending) {
						t.Fatal("cleanup decision mutated/recovered the pending erasure", err)
					}
				} else {
					for _, want := range []tasks.Item{user, other} {
						got, found, err := s.GetTask(ctx, want.ID)
						if err != nil || !found || !reflect.DeepEqual(got, want) {
							t.Fatal("cleanup changed unrelated durable task", got, want, found, err)
						}
					}
				}
			}
		})
	}
}

func TestReminderCleanupAuthenticatesUnrelatedMeetingAndStaleTaskReceipts(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	m := fixtureMeeting("unrelated-live-meeting")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	r, err := s.Append(ctx, fixtureMeetingFinal(m.ID, "one", "Unrelated saved meeting source"))
	if err != nil {
		t.Fatal(err)
	}
	live := cleanupTask(t, s, 1, r.SourceID)
	stale := live
	stale.Revision++
	missing := taskFixture(99)
	ids := []string{reminders.NotificationID(live), reminders.NotificationID(stale), reminders.NotificationID(missing)}
	got, err := s.CleanupReminderIDs(ctx, "other-meeting", ids)
	if err != nil || !slices.Equal(got, ids[1:]) {
		t.Fatal("unrelated authenticated membership was discarded or stale notice survived", got, err)
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return tx.deleteInferredIndex(taskSourceNS, r.SourceID)
	}); err != nil {
		t.Fatal(err)
	}
	got, err = s.CleanupReminderIDs(ctx, "other-meeting", ids[:1])
	if err != nil || !slices.Equal(got, ids[:1]) {
		t.Fatal("unprovable task/source reverse authority survived cleanup", got, err)
	}
}

func TestReminderCleanupBoundsNativeSnapshotAndCancellation(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	var ids []string
	for i := 1; i <= reminders.MaxSnapshotIDs; i++ {
		ids = append(ids, reminders.NotificationID(taskFixture(i)))
	}
	got, err := s.CleanupReminderIDs(ctx, "meeting", ids)
	if err != nil || !slices.Equal(got, ids) {
		t.Fatal("full bounded native snapshot exceeded transaction budget", len(got), err)
	}
	for _, invalid := range [][]string{append(slices.Clone(ids), reminders.NotificationID(taskFixture(len(ids)+1))), {ids[0], ids[0]}, {"invalid"}} {
		if got, err := s.CleanupReminderIDs(ctx, "meeting", invalid); !errors.Is(err, tasks.ErrInvalid) || len(got) != 0 {
			t.Fatal("unbounded/invalid request admitted", len(got), err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := s.CleanupReminderIDs(canceled, "meeting", ids); !errors.Is(err, context.Canceled) || len(got) != 0 {
		t.Fatal("canceled cleanup returned decisions", len(got), err)
	}
}

func TestReminderCleanupAppliesPendingCaptureScopeToCrossPartRangeSources(t *testing.T) {
	ctx := context.Background()
	s, _, _ := meetingStore(t)
	at := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	var items []tasks.Item
	var family string
	for i, word := range []string{"olderseamquartz", "newerseamquartz"} {
		text := strings.Repeat(" ", capture.MaxCapturePartTextBytes-5) + word + " ending."
		summary := publishIndexedFixture(t, s, text, at.Add(time.Duration(i)*2*time.Hour))
		family = summary.Header().FamilyID()
		drainCaptureIndex(t, s)
		page, err := s.SearchExact(ctx, scopedQuery(word))
		if err != nil || len(page.Hits) != 1 {
			t.Fatal("missing exact cross-part source fixture", page, err)
		}
		if err := s.view(ctx, func(reader bolt.Reader) error {
			mapping, found, err := s.captureIndexPartReader(reader, page.Hits[0].SourceID)
			if err == nil && (!found || !mapping.Range) {
				return ErrCorrupt
			}
			return err
		}); err != nil {
			t.Fatal("fixture is not an authenticated cross-part range", err)
		}
		items = append(items, cleanupTask(t, s, i+1, page.Hits[0].SourceID))
	}
	cutoff := at.Add(time.Hour)
	interrupted := errors.New("capture erasure interrupted before source cleanup")
	_, err := s.deleteCapturesWithHook(ctx, family, &cutoff, true, nil, func() error { return interrupted })
	if !errors.Is(err, interrupted) {
		t.Fatal("capture fixture did not leave a committed journal", err)
	}
	if _, _, err := s.GetTask(ctx, items[1].ID); !errors.Is(err, ErrDeletionPending) {
		t.Fatal("ordinary task read bypassed capture journal", err)
	}
	ids := []string{reminders.NotificationID(items[0]), reminders.NotificationID(items[1])}
	got, err := s.CleanupReminderIDs(ctx, "unrelated-meeting", ids)
	if err != nil || !slices.Equal(got, ids[:1]) {
		t.Fatal("cross-part range escaped original parent/cutoff authority", got, err)
	}
}

func TestReminderCleanupDoesNotWaitForActiveErasureWriter(t *testing.T) {
	s, _, _ := meetingStore(t)
	s.state.mu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := s.CleanupReminderIDs(context.Background(), "meeting", []string{reminders.NotificationID(taskFixture(1))})
		done <- err
	}()
	select {
	case err := <-done:
		s.state.mu.Unlock()
		if !errors.Is(err, ErrDeletionPending) {
			t.Fatal("active writer did not fail closed", err)
		}
	case <-time.After(100 * time.Millisecond):
		s.state.mu.Unlock()
		<-done
		t.Fatal("cleanup ignored the active erasure gate and waited for its writer")
	}
}
