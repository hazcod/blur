//go:build linux

package client

import (
	platformlinux "github.com/hazcod/blur/internal/platform/linux"
	"github.com/hazcod/blur/internal/reminders"
)

func newReminderPort() (reminders.NotificationPort, <-chan string) {
	port := platformlinux.NewNotifications()
	port.Start()
	return port, port.Opened()
}
