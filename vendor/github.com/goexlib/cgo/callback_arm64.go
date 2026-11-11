//go:build linux || darwin

package cgo

import (
	"reflect"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
)

// cbEntrySize is the size of one entry in goexlib_cgo_callbackasm
// (a MOVD plus a B instruction).
const cbEntrySize = 8

func cbNewDispatchTrampoline() uintptr {
	return purego.NewCallback(cbDispatch)
}

func buildCbArgPlan(typ reflect.Type, _ bool) []cbArgLoc {
	types := make([]reflect.Type, typ.NumIn())
	for i := range types {
		types[i] = typ.In(i)
	}
	return arm64ArgPlan(types)
}

func encodeCbStructRet(entry *cbEntry, v reflect.Value, f *cbFrame) {
	p, pin := cbStructImage(v)
	size := entry.ret.size
	switch entry.ret.kind {
	case cbRetStructHFA:
		// One element per float register: S0-S3 or D0-D3.
		for i := 0; i < entry.ret.hfaN; i++ {
			if entry.ret.hfaElem64 {
				f.retFlt[i] = uintptr(*(*uint64)(unsafe.Add(p, i*8)))
			} else {
				f.retFlt[i] = uintptr(*(*uint32)(unsafe.Add(p, i*4)))
			}
		}
	case cbRetStructInt:
		f.retInt[0] = cbChunk(p, 0, size)
		if size > 8 {
			f.retInt[1] = cbChunk(p, 8, size)
		}
	case cbRetStructMem:
		// The hidden result pointer arrived in X8.
		Memcpy(cbPointer(&f.x8), p, Sizet(size))
	}
	runtime.KeepAlive(pin)
}
