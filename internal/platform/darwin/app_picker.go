package darwin

import "errors"

// Application is a macOS application identified by its bundle identifier.
type Application struct {
	Name     string
	BundleID string
}

// ErrApplicationPickerUnavailable is returned when the application picker
// cannot run (non-macOS builds, or the AppKit classes are missing).
var ErrApplicationPickerUnavailable = errors.New("darwin: application picker unavailable")

// ErrApplicationPickerCancelled is returned when the user dismisses the panel.
var ErrApplicationPickerCancelled = errors.New("darwin: application picker cancelled")
