//go:build !arm64 || (!linux && !darwin)

package cgo

import "github.com/ebitengine/purego"

func registerFunc(destination any, address uintptr) {
	purego.RegisterFunc(destination, address)
}
