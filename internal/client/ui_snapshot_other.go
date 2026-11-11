//go:build !darwin

package client

import "errors"

const uiSnapshotSupported = false

func snapshotWindowToFile(uintptr, string) error { return errors.New("ui snapshot unsupported") }

func displayWindowNow(uintptr) {}
