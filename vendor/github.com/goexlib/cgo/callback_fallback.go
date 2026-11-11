//go:build !(((linux || darwin) && (amd64 || arm64)) || (windows && amd64))

package cgo

import "github.com/ebitengine/purego"

// newCallback forwards to purego.NewCallback on platforms where this
// package has no native callback thunk.
func newCallback(fn any) uintptr {
	return purego.NewCallback(fn)
}
