//go:build linux || darwin

package cgo

import (
	"fmt"
	"math"
	"reflect"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
)

// The native entry takes a single pointer through purego.SyscallN. This package
// owns the target function's ABI, while purego owns the runtime stack switch.
// Keep this capacity and the assembly's private stack area in sync.
const arm64CallStackBytes = 256

type arm64CallFrame struct {
	function  uintptr
	ints      [8]uintptr
	floats    [8]uintptr
	stack     [arm64CallStackBytes]byte
	stackSize uintptr
	x8        uintptr
	retInt    [2]uintptr
	retFloat  [4]uintptr
}

// Set by call_arm64.s; this is a C ABI entry, not a Go ABI function value.
var arm64CallEntryAddr uintptr

type arm64CallPlan struct {
	args []cbArgLoc
	ret  cbRetPlan
	typ  reflect.Type
}

func callRet[T any](function uintptr, args ...any) (ret T) {
	call(function, &ret, args...)
	return
}

func call(function uintptr, result any, args ...any) {
	values := make([]reflect.Value, len(args))
	for i, value := range args {
		values[i] = reflect.ValueOf(value)
	}
	var resultType reflect.Type
	if result != nil {
		resultType = reflect.TypeOf(result).Elem()
	}
	plan := newARM64CallPlan(valueTypes(values), resultType)
	value := plan.invoke(function, values)
	if result != nil {
		reflect.ValueOf(result).Elem().Set(value)
	}
}

func valueTypes(values []reflect.Value) []reflect.Type {
	types := make([]reflect.Type, len(values))
	for i, value := range values {
		if !value.IsValid() {
			panic("cgo.Call: untyped nil argument")
		}
		types[i] = value.Type()
	}
	return types
}

func newARM64CallPlan(types []reflect.Type, result reflect.Type) *arm64CallPlan {
	native := make([]reflect.Type, len(types))
	for i, typ := range types {
		checkARM64CallType(typ, false)
		native[i] = typ
		switch typ.Kind() {
		case reflect.String, reflect.Slice, reflect.Func:
			native[i] = reflect.TypeOf(uintptr(0))
		}
	}
	if result != nil {
		checkARM64CallType(result, true)
	}
	plan := &arm64CallPlan{args: arm64ArgPlan(native), typ: result}
	plan.ret, _ = buildCbRetPlan(result)
	for _, loc := range plan.args {
		size := loc.typ.Size()
		if loc.kind == cbArgIndirect {
			size = 8
		}
		if loc.src == cbSrcStack && loc.soff+size > arm64CallStackBytes {
			panic("cgo.Call: too many ARM64 stack arguments")
		}
	}
	return plan
}

func checkARM64CallType(typ reflect.Type, result bool) {
	switch typ.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Pointer, reflect.UnsafePointer, reflect.String:
	case reflect.Struct:
		checkCbStructFields(typ)
	case reflect.Slice, reflect.Func:
		if !result {
			return
		}
		fallthrough
	default:
		panic(fmt.Sprintf("cgo.Call: unsupported type %s", typ))
	}
}

func (plan *arm64CallPlan) invoke(function uintptr, values []reflect.Value) reflect.Value {
	if function == 0 {
		panic("cgo.Call: nil function address")
	}
	frame := &arm64CallFrame{function: function}
	var pin runtime.Pinner
	pin.Pin(frame)
	defer pin.Unpin()
	// All converted objects are owned by this invocation, including indirect
	// aggregates. Reentrant and concurrent calls never borrow another frame.
	owners := make([]any, 0, len(values)+1)
	defer func() { runtime.KeepAlive(values); runtime.KeepAlive(owners) }()
	for i, original := range values {
		value := original
		switch value.Kind() {
		case reflect.String:
			p := CStringTemp(value.String())
			pin.Pin(p)
			owners = append(owners, p)
			value = reflect.ValueOf(uintptr(unsafe.Pointer(p)))
		case reflect.Slice:
			if !value.IsNil() {
				pin.Pin(value.UnsafePointer())
			}
			value = reflect.ValueOf(value.Pointer())
		case reflect.Func:
			value = reflect.ValueOf(NewCallback(value.Interface()))
		}
		pinARM64Pointers(value, &pin)
		owners = append(owners, frame.put(plan.args[i], value, &pin))
	}
	var result reflect.Value
	if plan.typ != nil {
		result = reflect.New(plan.typ).Elem()
		if plan.ret.kind == cbRetStructMem {
			owner := result.Addr().Interface()
			pin.Pin(owner)
			owners = append(owners, owner)
			frame.x8 = result.Addr().Pointer()
		}
	}
	purego.SyscallN(arm64CallEntryAddr, uintptr(unsafe.Pointer(frame)))
	if plan.typ != nil && plan.ret.kind != cbRetStructMem {
		frame.result(result, plan.ret)
	}
	return result
}

func pinARM64Pointers(value reflect.Value, pin *runtime.Pinner) {
	switch value.Kind() {
	case reflect.Pointer, reflect.UnsafePointer:
		if !value.IsNil() {
			pin.Pin(value.UnsafePointer())
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			pinARM64Pointers(value.Field(i), pin)
		}
	case reflect.Array:
		for i := 0; i < value.Len(); i++ {
			pinARM64Pointers(value.Index(i), pin)
		}
	}
}

func (frame *arm64CallFrame) slot(source cbArgSrc, register int, offset uintptr, size uintptr) unsafe.Pointer {
	switch source {
	case cbSrcIntReg:
		return unsafe.Pointer(&frame.ints[register])
	case cbSrcFloatReg:
		return unsafe.Pointer(&frame.floats[register])
	default:
		if offset+size > arm64CallStackBytes {
			panic("cgo.Call: too many ARM64 stack arguments")
		}
		frame.stackSize = cbAlignUp(max(frame.stackSize, offset+size), 8)
		return unsafe.Add(unsafe.Pointer(&frame.stack[0]), offset)
	}
}

func (frame *arm64CallFrame) put(loc cbArgLoc, value reflect.Value, pin *runtime.Pinner) any {
	if loc.kind == cbArgZero {
		return nil
	}
	if value.Kind() != reflect.Struct {
		var word uintptr
		switch value.Kind() {
		case reflect.Bool:
			if value.Bool() {
				word = 1
			}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			word = uintptr(value.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			word = uintptr(value.Uint())
		case reflect.Float32:
			word = uintptr(math.Float32bits(float32(value.Float())))
		case reflect.Float64:
			word = uintptr(math.Float64bits(value.Float()))
		case reflect.Pointer, reflect.UnsafePointer:
			word = value.Pointer()
		}
		size := value.Type().Size()
		if loc.src != cbSrcStack {
			size = 8
		}
		copyARM64Bytes(frame.slot(loc.src, loc.reg, loc.soff, size), unsafe.Pointer(&word), size)
		return nil
	}
	owner := reflect.New(value.Type())
	owner.Elem().Set(value)
	source := owner.UnsafePointer()
	switch loc.kind {
	case cbArgIndirect:
		pin.Pin(owner.Interface())
		word := owner.Pointer()
		copyARM64Bytes(frame.slot(loc.src, loc.reg, loc.soff, 8), unsafe.Pointer(&word), 8)
	case cbArgAssemble:
		for _, piece := range loc.pieces {
			copyARM64Bytes(frame.slot(piece.src, piece.reg, piece.soff, piece.size), unsafe.Add(source, piece.doff), piece.size)
		}
	default:
		copyARM64Bytes(frame.slot(loc.src, loc.reg, loc.soff, loc.typ.Size()), source, loc.typ.Size())
	}
	return owner.Interface()
}

func copyARM64Bytes(destination, source unsafe.Pointer, size uintptr) {
	copy(unsafe.Slice((*byte)(destination), size), unsafe.Slice((*byte)(source), size))
}

func (frame *arm64CallFrame) result(result reflect.Value, plan cbRetPlan) {
	if result.Kind() == reflect.Bool {
		result.SetBool(uint8(frame.retInt[0]) != 0)
		return
	}
	if result.Kind() == reflect.String {
		result.SetString(GoString(cbPointer(&frame.retInt[0])))
		return
	}
	destination := result.Addr().UnsafePointer()
	switch plan.kind {
	case cbRetNone:
	case cbRetFloat:
		copyARM64Bytes(destination, unsafe.Pointer(&frame.retFloat[0]), result.Type().Size())
	case cbRetStructHFA:
		width := uintptr(4)
		if plan.hfaElem64 {
			width = 8
		}
		for i := 0; i < plan.hfaN; i++ {
			copyARM64Bytes(unsafe.Add(destination, uintptr(i)*width), unsafe.Pointer(&frame.retFloat[i]), width)
		}
	default:
		// A typed copy also applies the write barrier to pointer-bearing results.
		result.Set(reflect.NewAt(result.Type(), unsafe.Pointer(&frame.retInt[0])).Elem())
	}
}

func registerFunc(destination any, function uintptr) {
	value := reflect.ValueOf(destination)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Func || function == 0 {
		panic("cgo.RegisterFunc: expected a non-nil function pointer and address")
	}
	typ := value.Elem().Type()
	if typ.NumOut() > 1 {
		panic("cgo.RegisterFunc: at most one result is supported")
	}
	var resultType reflect.Type
	if typ.NumOut() == 1 {
		resultType = typ.Out(0)
	}
	inputs := make([]reflect.Type, typ.NumIn())
	for i := range inputs {
		inputs[i] = typ.In(i)
	}
	expand := len(inputs) > 0 && inputs[len(inputs)-1] == reflect.TypeOf([]any(nil))
	var plan *arm64CallPlan
	if expand {
		// Validate the static part now; the expanded types are known at call time.
		newARM64CallPlan(inputs[:len(inputs)-1], resultType)
	} else {
		plan = newARM64CallPlan(inputs, resultType)
	}
	value.Elem().Set(reflect.MakeFunc(typ, func(args []reflect.Value) []reflect.Value {
		current := plan
		if expand {
			last := args[len(args)-1].Interface().([]any)
			flat := make([]reflect.Value, len(args)-1, len(args)-1+len(last))
			copy(flat, args[:len(args)-1])
			for _, item := range last {
				flat = append(flat, reflect.ValueOf(item))
			}
			args = flat
			current = newARM64CallPlan(valueTypes(args), resultType)
		}
		result := current.invoke(function, args)
		if resultType == nil {
			return nil
		}
		return []reflect.Value{result}
	}))
}
