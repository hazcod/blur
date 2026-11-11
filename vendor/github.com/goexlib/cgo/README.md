# cgo

**Pure-Go FFI Library for Windows, Linux, and macOS - No CGO Required**

## Overview

**cgo** is a zero-c-dependency, pure-Go Foreign Function Interface (FFI) library that enables calling C shared libraries directly from Go code without requiring the CGO toolchain. Built on top of https://github.com/ebitengine/purego, it provides a unified API for Windows, Linux, and macOS with enhanced features for modern Go development.

And in addition, it adds support for **struct parameters and return values** on the Windows platform.

## Features
- 🚫 **No CGO** - Eliminates cross-compilation complexities and C toolchain dependencies
- 🎯 **Unified API** - Consistent interface across all supported platforms
- 📦 **Zero Dependencies** - Pure Go implementation, only standard library + purego
- 🔍 **Extern Variable Support** - Read and write C `extern` global variables
- 🚀 **Fast Development** - No recompilation needed when changing Go code
- 🏃 **Cross-Platform Support**:  Windows (`dll`), Linux (`so`), macOS (`dylib`)

## Quick Start

### Installation

```bash
go get github.com/goexlib/cgo
```

### 1. Basic Library Usage
```go
package main

import (
    "fmt"
    "github.com/goexlib/cgo"
)

func main() {
    // Load a shared library file
    lib, err := cgo.LoadLibrary("path/to/library.file")
    if err != nil {
        panic(err)
    }
    defer lib.Close()

    // Load a function symbol
    sym, err := lib.GetSymbol("add")
    if err != nil {
        panic(err)
    }

    // Call the C function
    result := cgo.CallRet[cgo.Int](sym.Addr(), cgo.Int(5), cgo.Int(3))
    fmt.Printf("5 + 3 = %d\n", result)
}
```

### 2. Working with Extern Variables
```go
package main

import (
    "fmt"
    "github.com/goexlib/cgo"
)

func main() {
    lib, err := cgo.LoadLibrary("mylib")
    if err != nil {
        panic(err)
    }
    defer lib.Close()
	
    // Access an extern integer variable
    counter,err := cgo.GetExternVariant[cgo.Int](lib, "counter")
    if err != nil {
        panic(err)
    }

    fmt.Printf("Counter value: %d\n", *counter) // 0
	
	fnIncrement, err := lib.GetSymbol("increment")
	if err != nil {
		panic(err)
	}
	cgo.Call(fnIncrement)
	
	fmt.Printf("Counter value: %d\n", *counter) // 1
	
	*counter = 999
	cgo.Call(fnIncrement)
	fmt.Printf("Counter value: %d\n", *counter) // 1000
}
```

### 3. Platform-Specific Code Paths
```go
package main

import (
    "runtime"
    "github.com/goexlib/cgo"
)

func main() {
    var libPath string
    
    switch runtime.GOOS {
    case "windows":
        libPath = "C:\\libs\\mylib.dll"
    case "darwin":
        libPath = "/usr/local/lib/libmylib.dylib"
    case "linux":
        libPath = "/usr/lib/libmylib.so"
    }
    
    lib, err := cgo.LoadLibrary(libPath)
    if err != nil {
        panic(err)
    }
    defer lib.Close()
    
    // Use library...
}
```

### 4. Callbacks with Float/Struct Arguments and Return Values

`cgo.NewCallback` converts a Go function into a C function pointer. It
implements the full platform C calling convention — including **float32/float64
and struct arguments and return values** — via this package's own assembly
thunks on linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 and
windows/amd64. (`purego.NewCallback` and `syscall.NewCallback` support neither
float/struct returns nor struct arguments.)

```go
package main

import "github.com/goexlib/cgo"

type Point struct {
    X, Y cgo.Float
}

func main() {
    // A callback taking and returning a struct:
    cb := cgo.NewCallback(func(p Point) Point {
        return Point{X: p.X * 2, Y: p.Y * 2}
    })

    // Pass cb to a C function expecting: Point (*)(Point)
    _ = cb
}
```

Supported callback signatures:

| | int/pointer/bool args | float args | struct args | float return | struct return |
|---|---|---|---|---|---|
| linux/amd64, linux/arm64 | ✅ | ✅ | ✅ | ✅ | ✅ |
| darwin/amd64, darwin/arm64 | ✅ | ✅ | ✅ | ✅ | ✅ |
| windows/amd64 | ✅ | ✅ | ✅ | ✅ | ✅ |
| other purego platforms | ✅ | ✅ | ❌ | ❌ | ❌ |

Struct types must have the same memory layout as the C struct (including
padding). Register/stack placement, homogeneous float aggregates, small
structs in registers, by-reference and by-value large structs, and the
hidden struct-return pointer all follow the platform C ABI (System V AMD64,
AAPCS64 incl. Apple's stack packing, and the Microsoft x64 convention).

## Platform-Specific Notes

For Go memory passed to native code, see the [pointer lifetime contract](doc/PointerLifetime.md).
Raw calls require pointer-to-uintptr conversions directly in the call arguments;
typed calls should receive pointers as pointers. Memory retained after return
needs an explicit owner and lifetime.

### Windows
- Searches in: application directory, system directories, `PATH`
- Supports both `stdcall` and `cdecl` calling conventions (X64 ABI)

### Linux
- Respects `LD_LIBRARY_PATH` environment variable

### macOS
- Searges in: `@rpath`, `@executable_path`, system directories
- Supports framework bundles

## License

This project is licensed under the MIT License
