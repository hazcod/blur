//go:build !darwin && !linux

package client

import "github.com/golang-gui/goui/ui"

func requestSyncFolder(_ ui.App, _ string, done func(string, []byte, error)) {
	done("", nil, ErrNotConfigured)
}
