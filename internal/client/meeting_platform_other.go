//go:build !darwin

package client

func installMeetingShortcut(func()) error                            { return nil }
func uninstallMeetingShortcut()                                      {}
func updateMeetingMenu(func(), func(), bool, bool, bool, bool) error { return nil }
