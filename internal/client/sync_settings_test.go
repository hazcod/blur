package client

import (
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/replica"
)

func TestSyncStatusLine(t *testing.T) {
	at := time.Date(2026, 11, 11, 10, 42, 0, 0, time.Local)
	cases := []struct {
		name string
		st   replica.State
		want string
	}{
		{"off", replica.State{}, "Off."},
		{"no folder", replica.State{Enabled: true}, "On. Choose a folder to start."},
		{"no key", replica.State{Enabled: true, FolderPath: "/x"}, "On. Set a passphrase to start."},
		{"no backup", replica.State{Enabled: true, FolderPath: "/x", KeySet: true}, "No backup yet. No other devices."},
		{"ready", replica.State{Enabled: true, FolderPath: "/x", KeySet: true, LastBackupAt: at.UTC(), OtherDevices: 2}, "Last backup 10:42. 2 other devices."},
		{"one", replica.State{Enabled: true, FolderPath: "/x", KeySet: true, LastBackupAt: at.UTC(), OtherDevices: 1}, "Last backup 10:42. 1 other device."},
	}
	for _, tc := range cases {
		if got := syncStatusLine(tc.st); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSyncActionMessagesAreContentFree(t *testing.T) {
	for _, err := range []error{replica.ErrNoKey, replica.ErrAuth, replica.ErrFolderNotReady, replica.ErrMemoryChanging, errTest("/Users/secret/path")} {
		if msg := syncActionMessage(err); msg == "" || strings.Contains(msg, "/Users") {
			t.Errorf("message for %v = %q", err, msg)
		}
	}
	if syncActionMessage(nil) != "" {
		t.Error("nil error must have no message")
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
