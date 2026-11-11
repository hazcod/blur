package cgo

import (
	"errors"
	"fmt"
)

func RegisterFunc(pf any, cfn uintptr) (err error) {
	defer func() {
		if msg := recover(); msg != nil {
			err = errors.New(fmt.Sprint(msg))
		}
	}()
	registerFunc(pf, cfn)
	return
}

func RegisterLibFunc(pf any, lib uintptr, symbol string) (err error) {
	defer func() {
		if msg := recover(); msg != nil {
			err = errors.New(fmt.Sprint(msg))
		}
	}()
	address, err := Dlsym(lib, symbol)
	if err != nil {
		return err
	}
	registerFunc(pf, address)
	return
}
