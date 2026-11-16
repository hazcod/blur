package reminders

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/tasks"
)

var (
	ErrPermission     = errors.New("reminders: notifications not allowed")
	ErrUnavailable    = errors.New("reminders: notification service unavailable")
	ErrUnsupported    = errors.New("reminders: notifications unsupported")
	ErrOutcomeUnknown = errors.New("reminders: notification scheduling outcome unknown")
	ErrMoreWork       = errors.New("reminders: more saved tasks to reconcile")
)

type Authorization string

const (
	NotDetermined Authorization = "not_determined"
	Denied        Authorization = "denied"
	Allowed       Authorization = "allowed"
	Unsupported   Authorization = "unsupported"
)

const NotificationPrefix = "blur.reminder."
const MaxSnapshotIDs = 1024
const NotificationContentVersion uint8 = 1

type Notice struct {
	ID       string
	Title    string
	Schedule tasks.Reminder
}
type Snapshot struct {
	Pending, Delivered []string
	NextAt             map[string]time.Time
	// Missing keys mean the port does not report payload versions. Explicit zero
	// identifies a legacy generic pending payload that can be upgraded in place.
	ContentVersions map[string]uint8
}

// NotificationPort schedules the explicit task title as visible reminder text.
// Captured source text and note content are not included. The OS may retain the
// queued title and control whether notification previews are visible.
type NotificationPort interface {
	Authorization(context.Context) (Authorization, error)
	RequestAuthorization(context.Context) (Authorization, error)
	Snapshot(context.Context) (Snapshot, error)
	Queue(context.Context, Notice) error
	Remove(context.Context, []string) error
	RemoveAll(context.Context) error
}

func NotificationID(item tasks.Item) string {
	return NotificationPrefix + item.ID + "." + strconv.FormatUint(item.Revision, 10)
}
func NotificationTaskID(id string) (string, uint64, bool) {
	if !strings.HasPrefix(id, NotificationPrefix) {
		return "", 0, false
	}
	parts := strings.Split(strings.TrimPrefix(id, NotificationPrefix), ".")
	if len(parts) != 2 || !tasks.ValidID(parts[0]) {
		return "", 0, false
	}
	revision, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || revision == 0 || revision > 1<<48 || strconv.FormatUint(revision, 10) != parts[1] {
		return "", 0, false
	}
	return parts[0], revision, true
}

// UnavailablePort keeps task persistence usable on platforms without this
// notification adapter. It can never have queued notices to cancel.
type UnavailablePort struct{}

func (UnavailablePort) Authorization(context.Context) (Authorization, error) { return Unsupported, nil }
func (UnavailablePort) RequestAuthorization(context.Context) (Authorization, error) {
	return Unsupported, nil
}
func (UnavailablePort) Snapshot(context.Context) (Snapshot, error) { return Snapshot{}, nil }
func (UnavailablePort) Queue(context.Context, Notice) error        { return ErrUnsupported }
func (UnavailablePort) Remove(context.Context, []string) error     { return nil }
func (UnavailablePort) RemoveAll(context.Context) error            { return nil }
