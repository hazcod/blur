//go:build darwin

package client

import (
	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/reminders"
)

func newReminderPort() (reminders.NotificationPort, <-chan string) {
	port := darwin.NewNotifications()
	_ = port.Prepare()
	return port, port.Opened()
}
