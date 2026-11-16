//go:build !darwin

package client

func notificationSettingsSupported() bool { return false }
func openNotificationSettings() error     { return ErrNotConfigured }
