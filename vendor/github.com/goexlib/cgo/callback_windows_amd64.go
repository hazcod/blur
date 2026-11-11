package cgo

import (
	"reflect"
	"runtime"
	"syscall"
)

// cbEntrySize is the size of one entry in goexlib_cgo_callbackasm
// (a CALL rel32 instruction).
const cbEntrySize = 5

func cbNewDispatchTrampoline() uintptr {
	return syscall.NewCallback(cbDispatch)
}

// buildCbArgPlan assigns the arguments following the Microsoft x64 calling
// convention: argument slots are positional, slot i (i < 4) lives in XMM_i
// for floats or in RCX,RDX,R8,R9 for everything else, and the remaining
// slots go onto the stack in 8-byte cells. A struct of size 1, 2, 4 or 8
// bytes is passed by value in one slot; any other struct is passed by
// reference (a pointer in one slot).
func buildCbArgPlan(ty reflect.Type, sret bool) []cbArgLoc {
	pos := 0
	if sret {
		pos = 1 // hidden struct return pointer in RCX
	}
	locs := make([]cbArgLoc, ty.NumIn())
	for i := range locs {
		t := ty.In(i)
		locs[i].typ = t
		kind := cbArgDirect
		isFloat := t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64
		if t.Kind() == reflect.Struct {
			isFloat = false // structs always use an integer slot on win64
			switch t.Size() {
			case 0:
				locs[i].kind = cbArgZero
				continue
			case 1, 2, 4, 8:
				// passed by value in one slot
			default:
				kind = cbArgIndirect // passed by reference
			}
		}
		locs[i].kind = kind
		if pos < 4 {
			if isFloat {
				locs[i].src = cbSrcFloatReg
			} else {
				locs[i].src = cbSrcIntReg
			}
			locs[i].reg = pos
		} else {
			locs[i].src = cbSrcStack
			locs[i].soff = uintptr(pos-4) * 8
		}
		pos++
	}
	return locs
}

// buildCbRetPlan classifies the return value following the Microsoft x64
// calling convention: floats return in XMM0, structs of size 1, 2, 4 or 8
// bytes return in RAX and all other structs are copied through a hidden
// pointer passed in RCX which must be handed back in RAX.
func buildCbRetPlan(t reflect.Type) (plan cbRetPlan, sret bool) {
	if t == nil {
		plan.kind = cbRetNone
		return
	}
	switch t.Kind() {
	case reflect.Float32, reflect.Float64:
		plan.kind = cbRetFloat
	case reflect.Struct:
		plan.size = t.Size()
		switch plan.size {
		case 0:
			plan.kind = cbRetNone
		case 1, 2, 4, 8:
			plan.kind = cbRetStructInt
		default:
			plan.kind = cbRetStructMem
			sret = true
		}
	default:
		plan.kind = cbRetInt
	}
	return
}

func encodeCbStructRet(entry *cbEntry, v reflect.Value, f *cbFrame) {
	p, pin := cbStructImage(v)
	size := entry.ret.size
	switch entry.ret.kind {
	case cbRetStructInt:
		f.retInt[0] = cbChunk(p, 0, size)
	case cbRetStructMem:
		// The hidden result pointer arrived in RCX (ints[0]) and must be
		// returned in RAX.
		Memcpy(cbPointer(&f.ints[0]), p, Sizet(size))
		f.retInt[0] = f.ints[0]
	}
	runtime.KeepAlive(pin)
}
