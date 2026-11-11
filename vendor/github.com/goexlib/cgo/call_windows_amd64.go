package cgo

import (
	"math"
	"reflect"
	"runtime"
	"syscall"
	"unsafe"
)

func callRet[T any](fn uintptr, args ...any) (ret T) {
	call(fn, &ret, args...)
	return
}

func call(fn uintptr, ret any, args ...any) {
	const PointerSize = unsafe.Sizeof(uintptr(0))
	// rawArgs contains integers, not GC roots. Preserve the typed arguments and
	// pin the buffers whose addresses the Microsoft x64 ABI passes indirectly.
	var pin runtime.Pinner
	defer pin.Unpin()
	defer runtime.KeepAlive(args)
	defer runtime.KeepAlive(ret)

	rawArgs := make([]uintptr, 0, len(args)+1)
	var retType reflect.Type
	if ret != nil {
		retType = reflect.TypeOf(ret).Elem()
		checkReturnType(retType)

		if retType.Kind() != reflect.String && retType.Size() > PointerSize {
			pin.Pin(ret)
			rp := (*iface)(unsafe.Pointer(&ret)).val
			rawArgs = append(rawArgs, uintptr(rp))
		}
	}

	for _, arg := range args {
		argType := reflect.TypeOf(arg)
		checkCgoType(argType)

		argVal := reflect.ValueOf(arg)
		switch argType.Kind() {
		case reflect.String:
			p := CStringTemp(argVal.String())
			pin.Pin(p)
			rawArgs = append(rawArgs, uintptr(p))
		case reflect.Bool:
			rawArgs = append(rawArgs, uintptr(CBool(argVal.Bool())))
		case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			rawArgs = append(rawArgs, uintptr(argVal.Int()))
		case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			rawArgs = append(rawArgs, uintptr(argVal.Uint()))
		case reflect.Float32:
			rawArgs = append(rawArgs, uintptr(math.Float32bits(float32(argVal.Float()))))
		case reflect.Float64:
			rawArgs = append(rawArgs, uintptr(math.Float64bits(argVal.Float())))
		case reflect.Pointer, reflect.UnsafePointer:
			if !argVal.IsNil() {
				pin.Pin(arg)
			}
			rawArgs = append(rawArgs, argVal.Pointer())
		default:
			v := (*iface)(unsafe.Pointer(&arg))
			if size := argType.Size(); size <= PointerSize {
				rawArgs = append(rawArgs, 0)
				Memcpy(unsafe.Pointer(&rawArgs[len(rawArgs)-1]), v.val, Sizet(argType.Size()))
			} else {
				// pass address
				pin.Pin(v.val)
				rawArgs = append(rawArgs, uintptr(v.val))
			}
		}
	}

	r1, r2, _ := syscall.SyscallN(fn, rawArgs...)
	if ret != nil {
		retVal := reflect.ValueOf(ret).Elem()
		switch retType.Kind() {
		case reflect.String:
			retVal.SetString(GoString(unsafe.Pointer(r1)))
		case reflect.Bool:
			retVal.SetBool(r1 != 0)
		case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			retVal.SetInt(int64(r1))
		case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			retVal.SetUint(uint64(r1))
		case reflect.Float32:
			retVal.SetFloat(float64(math.Float32frombits(uint32(r2))))
		case reflect.Float64:
			retVal.SetFloat(math.Float64frombits(uint64(r2)))
		default:
			if size := retType.Size(); size <= PointerSize {
				rv := (*iface)(unsafe.Pointer(&ret)).val
				Memcpy(rv, unsafe.Pointer(&r1), Sizet(size))
			}
		}
	}
}

func checkReturnType(retType reflect.Type) {
	switch retType.Kind() {
	case reflect.Array:
		panic("cgo.Call: unsupported return type")
	default:
		checkCgoType(retType)
	}
}

func checkCgoType(typ reflect.Type) {
	switch typ.Kind() {
	case reflect.Slice, reflect.Interface, reflect.Map, reflect.Chan, reflect.Func:
		panic("cgo.Call: unsupported cgo type")
	case reflect.Array:
		checkCgoType(typ.Elem())
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			checkCgoType(typ.Field(i).Type)
		}
	}
}

type iface struct {
	typ unsafe.Pointer
	val unsafe.Pointer
}
