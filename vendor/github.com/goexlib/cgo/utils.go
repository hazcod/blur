package cgo

var (
	libc   = NewLazyLibrary("libc.so.6", "msvcrt.dll", "/usr/lib/libSystem.B.dylib")
	malloc = libc.NewSymbol("malloc")
	free   = libc.NewSymbol("free")
	memset = libc.NewSymbol("memset")
)

func Malloc(size Sizet) Pointer {
	ret, _, _ := malloc.CallRaw(uintptr(size))
	return Pointer(ret)
}

func Free(p Pointer) {
	free.CallRaw(uintptr(p))
}

func Memset(p Pointer, val int, size Sizet) {
	memset.CallRaw(uintptr(p), uintptr(val), uintptr(size))
}

func Memcpy(dst, src Pointer, size Sizet) {
	copy(GoSliceNTemp[byte](dst, int(size)), GoSliceNTemp[byte](src, int(size)))
}
