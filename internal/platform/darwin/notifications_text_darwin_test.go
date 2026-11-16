package darwin_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

// This port is entirely synthetic. It never reaches UNUserNotificationCenter.
type reminderTextPort struct {
	notice reminders.Notice
	fail   bool
}

func (*reminderTextPort) Authorization(context.Context) (reminders.Authorization, error) {
	return reminders.Allowed, nil
}
func (p *reminderTextPort) RequestAuthorization(ctx context.Context) (reminders.Authorization, error) {
	return p.Authorization(ctx)
}
func (p *reminderTextPort) Snapshot(context.Context) (reminders.Snapshot, error) {
	return reminders.Snapshot{}, nil
}
func (p *reminderTextPort) Queue(_ context.Context, notice reminders.Notice) error {
	if p.fail {
		return reminders.ErrUnavailable
	}
	p.notice = notice
	return nil
}
func (*reminderTextPort) Remove(context.Context, []string) error { return nil }
func (*reminderTextPort) RemoveAll(context.Context) error        { return nil }

func TestPersistedReminderTaskTextReachesNativeContentAfterRetryAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic-memory.db")
	repo, err := durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	port := &reminderTextPort{fail: true}
	service, err := reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Create(context.Background(), "Pick up the drycleaning", &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "Europe/Brussels"}, "", false)
	if err != nil || !errors.Is(result.ScheduleError, reminders.ErrUnavailable) {
		t.Fatal("synthetic retry failure not persisted", err)
	}
	service.Close()
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	port.fail = false
	service, err = reminders.NewService(repo, port, reminders.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err := service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	persisted, found, err := repo.GetTask(context.Background(), result.Item.ID)
	if err != nil || !found || port.notice.Title != persisted.Title || port.notice.Title != result.Item.Title || port.notice.ID != reminders.NotificationID(persisted) {
		t.Fatal("encrypted reopen/retry lost intended notification text", err)
	}
	title, body, version, err := darwin.ReminderContentForTest(port.notice)
	if err != nil || title != "Reminder" || body != persisted.Title || version != reminders.NotificationContentVersion {
		t.Fatal("native content object lost persisted task title or version", err)
	}
}

func TestNativeReminderContentRejectsMissingTextAndEscapesLiteralPercent(t *testing.T) {
	notice := reminders.Notice{ID: reminders.NotificationPrefix + "1234567890abcdef1234567890abcdef.5", Title: "Check 95% rollout and literal %s", Schedule: tasks.Reminder{DueAt: time.Now().UTC().Add(time.Hour), Timezone: "UTC"}}
	_, body, _, err := darwin.ReminderContentForTest(notice)
	if err != nil {
		t.Fatal(err)
	}
	if body != "Check 95%% rollout and literal %%s" {
		t.Fatal("notification percent escaping changed intended literal text")
	}
	for _, invalid := range []string{"", "line\nbreak", strings.Repeat("x", tasks.MaxTitleBytes+1)} {
		notice.Title = invalid
		if _, _, _, err := darwin.ReminderContentForTest(notice); !errors.Is(err, tasks.ErrInvalid) {
			t.Fatal("missing/invalid task text silently became a generic notification")
		}
	}
}
