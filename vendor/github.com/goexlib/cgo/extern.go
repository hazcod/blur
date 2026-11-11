package cgo

func GetExternVariant[T any, L ~uintptr](lib L, name string) (pv *T, err error) {
	symbol, err := Dlsym(uintptr(lib), name)
	if err != nil {
		return
	}
	return (*T)(Pointer(symbol)), nil
}
