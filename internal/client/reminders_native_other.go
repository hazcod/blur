//go:build !darwin && !linux

package client

import "github.com/hazcod/blur/internal/reminders"

func newReminderPort() (reminders.NotificationPort, <-chan string) {
	return reminders.UnavailablePort{}, nil
}
