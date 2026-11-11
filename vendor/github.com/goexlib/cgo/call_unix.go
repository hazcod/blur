//go:build !windows

package cgo

import "github.com/ebitengine/purego"

func call(fn uintptr, ret any, args ...any) {
	var cfunc func(args ...any)
	purego.RegisterFunc(&cfunc, fn)
	cfunc(args...)
}

func callRet[T any](fn uintptr, args ...any) (ret T) {
	var cfunc func(args ...any) T
	purego.RegisterFunc(&cfunc, fn)
	return cfunc(args...)
}
