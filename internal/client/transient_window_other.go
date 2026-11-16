//go:build !darwin

package client

import "github.com/golang-gui/goui/ui"

func prepareDesktopTransient(ui.App, string) error { return nil }
func resizeMeetingOverlay(ui.App, float64) error   { return nil }
