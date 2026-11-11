//go:build !darwin

package darwin

import "errors"

// SnapshotWindow is a developer-only macOS facility; other systems report it
// as unavailable.
func SnapshotWindow(uintptr, string) error {
	return errors.New("darwin: window snapshot unavailable on this platform")
}
