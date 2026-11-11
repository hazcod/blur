package cgo

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

type LazyLibrary struct {
	mtx    sync.Mutex
	handle uintptr
	names  []string
}

func NewLazyLibrary(name string, fallbacks ...string) (l *LazyLibrary) {
	l = new(LazyLibrary)
	l.names = make([]string, 1+len(fallbacks))
	l.names[0] = name
	copy(l.names[1:], fallbacks)
	return
}

func (l *LazyLibrary) Load() error {
	if atomic.LoadUintptr(&l.handle) == 0 {
		l.mtx.Lock()
		defer l.mtx.Unlock()
		if l.handle == 0 {
			var (
				lib uintptr
				err error
				msg strings.Builder
			)
			for _, name := range l.names {
				lib, err = Dlopen(name, RTLD_NOW)
				if err != nil {
					fmt.Fprintf(&msg, " (%s: %v)", name, err)
					continue
				}
				msg.Reset()
				break
			}
			if msg.Len() != 0 {
				return fmt.Errorf("load lazy library err:%s", msg.String())
			}
			atomic.StoreUintptr(&l.handle, lib)
		}
	}
	return nil
}

func (l *LazyLibrary) Handle() uintptr {
	if err := l.Load(); err != nil {
		panic(err)
	}
	return l.handle
}

func (l *LazyLibrary) NewSymbol(name string) *LazySymbol {
	return &LazySymbol{lib: l, name: name}
}

type LazySymbol struct {
	mtx  sync.Mutex
	sym  uintptr
	lib  *LazyLibrary
	name string
}

func (s *LazySymbol) Find() error {
	if atomic.LoadUintptr(&s.sym) == 0 {
		s.mtx.Lock()
		defer s.mtx.Unlock()
		if s.sym == 0 {
			err := s.lib.Load()
			if err != nil {
				return err
			}
			sym, err := Dlsym(s.lib.handle, s.name)
			if err != nil {
				return err
			}
			atomic.StoreUintptr(&s.sym, sym)
		}
	}
	return nil
}

func (s *LazySymbol) Addr() uintptr {
	if err := s.Find(); err != nil {
		panic(err)
	}
	return s.sym
}

// CallRaw resolves the symbol and calls it synchronously. It has the same
// pointer lifetime contract as the package function, including during symbol
// resolution, which may allocate or grow the Go stack before the native call.
//
//go:uintptrescapes
func (s *LazySymbol) CallRaw(args ...uintptr) (r1, r2 uintptr, err error) {
	if err = s.Find(); err != nil {
		return 0, 0, err
	}

	r1, r2, ec := CallRaw(s.sym, args...)
	if isWindows {
		if ec != 0 {
			return r1, r2, ec
		}
	}

	return r1, r2, nil
}

const isWindows = runtime.GOOS == "windows"
