package cgo

// Call calls a C function synchronously. Pass pointer arguments as pointers,
// not uintptr: typed arguments preserve Go ownership through the native call.
// Native retention beyond the call needs a separate owner and lifetime.
func Call[F ~uintptr](fn F, args ...any) {
	call(uintptr(fn), nil, args...)
}

// CallRet calls a C function and returns a value. Pointer arguments have the
// same lifetime contract as Call.
func CallRet[T any, F ~uintptr](fn F, args ...any) (ret T) {
	return callRet[T](uintptr(fn), args...)
}
