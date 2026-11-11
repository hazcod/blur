//go:build ios || android

package nanovgo

import (
	"fmt"
	"log"
	"unsafe"
)

type Float float32

var shaderHeader string = `#version 300 es
#define NANOVG_GL3 1
#define UNIFORMARRAY_SIZE 11
`

func prepareTextureBuffer(data []byte, w, h, bpp int) []byte {
	return data
}

func castFloat32ToByte(vertexes []float32) []byte {
	// Convert []float32 list to []byte without copy
	var b []byte
	b = (*(*[1 << 20]byte)(unsafe.Pointer(&vertexes[0])))[:len(vertexes)*4]
	return b
}

func dumpLog(format string, args ...interface{}) {
	log.Output(2, fmt.Sprintf(format, args...))
}
