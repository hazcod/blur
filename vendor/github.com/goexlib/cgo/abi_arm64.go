//go:build linux || darwin

package cgo

import (
	"reflect"
	"runtime"
)

// aapcsIntRegs and aapcsFltRegs are the number of integer and float argument
// registers on the AAPCS64 calling convention (X0-X7 and D0-D7).
const (
	aapcsIntRegs = 8
	aapcsFltRegs = 8
)

// arm64ArgPlan assigns call and callback arguments following the AAPCS64 calling
// convention: integer-like arguments take X0-X7, float arguments take
// D0-D7 and the rest goes onto the stack. Stack size and alignment follow
// standard AAPCS64 or Apple ARM64 rules, as appropriate. Structs are
// classified per the ABI (see classifyStructArg).
func arm64ArgPlan(types []reflect.Type) []cbArgLoc {
	ints, floats := 0, 0
	// The hidden struct return pointer uses the dedicated X8 register on
	// arm64, so sret does not consume an argument register.
	var stackOff uintptr
	locs := make([]cbArgLoc, len(types))
	for i := range locs {
		t := types[i]
		locs[i].typ = t
		if t.Kind() == reflect.Struct {
			locs[i] = classifyStructArg(t, &ints, &floats, &stackOff)
			continue
		}
		isFloat := t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64
		if isFloat && floats < aapcsFltRegs {
			locs[i].src = cbSrcFloatReg
			locs[i].reg = floats
			floats++
			continue
		}
		if !isFloat && ints < aapcsIntRegs {
			locs[i].src = cbSrcIntReg
			locs[i].reg = ints
			ints++
			continue
		}
		locs[i].src = cbSrcStack
		locs[i].soff = aapcsStackPush(t, &stackOff)
	}
	return locs
}

// aapcsStackPush reserves a stack slot for a value passed by value. Apple
// packs scalars and HFAs naturally; ordinary composites retain eight-byte
// alignment and rounded size. Standard AAPCS64 also rounds scalar slots.
func aapcsStackPush(t reflect.Type, stackOff *uintptr) uintptr {
	size, align := t.Size(), uintptr(t.Align())
	natural := runtime.GOOS == "darwin" || runtime.GOOS == "ios"
	if natural && t.Kind() == reflect.Struct {
		natural, _, _ = cbIsHFA(t)
	}
	if !natural {
		if align < 8 {
			align = 8
		}
		size = cbAlignUp(size, 8)
	}
	off := cbAlignUp(*stackOff, align)
	*stackOff = off + size
	return off
}

// classifyStructArg places a struct argument following the AAPCS64 calling
// convention. An HFA (<=4 same-typed float members) is passed in float
// registers; a non-HFA struct up to 16 bytes is passed in integer registers;
// a larger struct is passed by reference (a pointer in a register or on the
// stack). Anything that does not fit in registers is passed on the stack.
func classifyStructArg(t reflect.Type, ints, floats *int, stackOff *uintptr) cbArgLoc {
	size := t.Size()
	if size == 0 {
		return cbArgLoc{typ: t, kind: cbArgZero}
	}
	if ok, n, elem64 := cbIsHFA(t); ok {
		if *floats+n <= aapcsFltRegs {
			memSize := uintptr(4)
			if elem64 {
				memSize = 8
			}
			pieces := make([]cbArgPiece, n)
			for i := 0; i < n; i++ {
				pieces[i] = cbArgPiece{src: cbSrcFloatReg, reg: *floats, doff: uintptr(i) * memSize, size: memSize}
				*floats++
			}
			return cbArgLoc{typ: t, kind: cbArgAssemble, pieces: pieces}
		}
		*floats = aapcsFltRegs // NSRN = 8: later float args also go on the stack
		return cbArgLoc{typ: t, kind: cbArgDirect, src: cbSrcStack, soff: aapcsStackPush(t, stackOff)}
	}
	if size <= 16 {
		n := int((size + 7) / 8)
		if *ints+n <= aapcsIntRegs {
			pieces := make([]cbArgPiece, n)
			for c := 0; c < n; c++ {
				sz := size - uintptr(c)*8
				if sz > 8 {
					sz = 8
				}
				pieces[c] = cbArgPiece{src: cbSrcIntReg, reg: *ints, doff: uintptr(c) * 8, size: sz}
				*ints++
			}
			return cbArgLoc{typ: t, kind: cbArgAssemble, pieces: pieces}
		}
		*ints = aapcsIntRegs // NGRN = 8: later integer args also go on the stack
		return cbArgLoc{typ: t, kind: cbArgDirect, src: cbSrcStack, soff: aapcsStackPush(t, stackOff)}
	}
	// Larger than 16 bytes: passed by reference. The pointer takes the next
	// integer register, or a stack slot when the integer registers are full.
	if *ints < aapcsIntRegs {
		loc := cbArgLoc{typ: t, kind: cbArgIndirect, src: cbSrcIntReg, reg: *ints}
		*ints++
		return loc
	}
	// The lowered argument is a pointer, including its stack alignment.
	loc := cbArgLoc{typ: t, kind: cbArgIndirect, src: cbSrcStack, soff: aapcsStackPush(reflect.TypeOf(uintptr(0)), stackOff)}
	return loc
}

// buildCbRetPlan classifies the return value following AAPCS64:
// floats return in D0, homogeneous float aggregates with up to four
// elements return in S0-S3/D0-D3, other structs up to 16 bytes return in
// X0,X1 and larger structs are copied through the pointer passed in X8.
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
		if plan.size == 0 {
			plan.kind = cbRetNone
		} else if ok, n, elem64 := cbIsHFA(t); ok {
			plan.kind = cbRetStructHFA
			plan.hfaN = n
			plan.hfaElem64 = elem64
		} else if plan.size <= 16 {
			plan.kind = cbRetStructInt
		} else {
			plan.kind = cbRetStructMem
			sret = true
		}
	default:
		plan.kind = cbRetInt
	}
	return
}

// cbIsHFA reports whether t is a homogeneous float aggregate: up to four
// fields (flattened through nested structs and arrays) that are all
// float32 or all float64.
func cbIsHFA(t reflect.Type) (ok bool, n int, elem64 bool) {
	kind := reflect.Invalid
	ok = true
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		if !ok {
			return
		}
		switch t.Kind() {
		case reflect.Float32, reflect.Float64:
			if kind == reflect.Invalid {
				kind = t.Kind()
			}
			if t.Kind() != kind {
				ok = false
			}
			n++
		case reflect.Struct:
			for i := 0; i < t.NumField(); i++ {
				walk(t.Field(i).Type)
			}
		case reflect.Array:
			for i := 0; i < t.Len(); i++ {
				walk(t.Elem())
			}
		default:
			ok = false
		}
	}
	walk(t)
	if !ok || n == 0 || n > 4 {
		return false, 0, false
	}
	return true, n, kind == reflect.Float64
}
