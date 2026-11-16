package client

import "github.com/hazcod/blur/internal/platform/darwin"

func notificationSettingsSupported() bool { return true }
func openNotificationSettings() error     { return darwin.OpenNotificationSettings() }
