package gl

import (
	"fmt"
	"github.com/goexlib/cgo"
)

var (
	load LoadFunc

	glGetError                 cgo.Symbol
	glBindTexture              cgo.Symbol
	glDeleteTextures           cgo.Symbol
	glGenTextures              cgo.Symbol
	glActiveTexture            cgo.Symbol
	glTexImage2D               cgo.Symbol
	glTexSubImage2D            cgo.Symbol
	glTexParameteri            cgo.Symbol
	glBlendFunc                cgo.Symbol
	glBlendFuncSeparate        cgo.Symbol
	glCreateProgram            cgo.Symbol
	glDeleteProgram            cgo.Symbol
	glGetProgramiv             cgo.Symbol
	glGetProgramInfoLog        cgo.Symbol
	glAttachShader             cgo.Symbol
	glGetAttribLocation        cgo.Symbol
	glBindAttribLocation       cgo.Symbol
	glLinkProgram              cgo.Symbol
	glUseProgram               cgo.Symbol
	glGetUniformLocation       cgo.Symbol
	glCreateShader             cgo.Symbol
	glDeleteShader             cgo.Symbol
	glGetShaderInfoLog         cgo.Symbol
	glShaderSource             cgo.Symbol
	glCompileShader            cgo.Symbol
	glGetShaderiv              cgo.Symbol
	glGenVertexArrays          cgo.Symbol
	glBindVertexArray          cgo.Symbol
	glGenBuffers               cgo.Symbol
	glBindBuffer               cgo.Symbol
	glDeleteBuffers            cgo.Symbol
	glBufferData               cgo.Symbol
	glEnableVertexAttribArray  cgo.Symbol
	glDisableVertexAttribArray cgo.Symbol
	glVertexAttribPointer      cgo.Symbol
	glDeleteVertexArrays       cgo.Symbol
	glPixelStorei              cgo.Symbol
	glGenerateMipmap           cgo.Symbol
	glUniform1i                cgo.Symbol
	glUniform1fv               cgo.Symbol
	glUniform2fv               cgo.Symbol
	glUniform4fv               cgo.Symbol
	glEnable                   cgo.Symbol
	glDisable                  cgo.Symbol
	glColorMask                cgo.Symbol
	glStencilMask              cgo.Symbol
	glStencilFunc              cgo.Symbol
	glStencilOp                cgo.Symbol
	glStencilOpSeparate        cgo.Symbol
	glDrawArrays               cgo.Symbol
	glCullFace                 cgo.Symbol
	glFrontFace                cgo.Symbol
	glFinish                   cgo.Symbol
	glViewport                 cgo.Symbol
	glClear                    cgo.Symbol
	glClearColor               cgo.Symbol
	glClearBufferfv            cgo.Symbol
)

func loadGlFuncs(loadFn LoadFunc) (err error) {
	load = loadFn
	defer func() {
		if e := recover(); e != nil {
			err, _ = e.(error)
		}
	}()

	glGetError = loadGlFunc("glGetError")
	glBindTexture = loadGlFunc("glBindTexture")
	glDeleteTextures = loadGlFunc("glDeleteTextures")
	glGenTextures = loadGlFunc("glGenTextures")
	glActiveTexture = loadGlFunc("glActiveTexture")
	glTexImage2D = loadGlFunc("glTexImage2D")
	glTexSubImage2D = loadGlFunc("glTexSubImage2D")
	glTexParameteri = loadGlFunc("glTexParameteri")
	glBlendFunc = loadGlFunc("glBlendFunc")
	glBlendFuncSeparate = loadGlFunc("glBlendFuncSeparate")
	glCreateProgram = loadGlFunc("glCreateProgram")
	glDeleteProgram = loadGlFunc("glDeleteProgram")
	glGetProgramiv = loadGlFunc("glGetProgramiv")
	glGetProgramInfoLog = loadGlFunc("glGetProgramInfoLog")
	glAttachShader = loadGlFunc("glAttachShader")
	glAttachShader = loadGlFunc("glAttachShader")
	glGetAttribLocation = loadGlFunc("glGetAttribLocation")
	glBindAttribLocation = loadGlFunc("glBindAttribLocation")
	glLinkProgram = loadGlFunc("glLinkProgram")
	glUseProgram = loadGlFunc("glUseProgram")
	glGetUniformLocation = loadGlFunc("glGetUniformLocation")
	glCreateShader = loadGlFunc("glCreateShader")
	glDeleteShader = loadGlFunc("glDeleteShader")
	glGetShaderInfoLog = loadGlFunc("glGetShaderInfoLog")
	glShaderSource = loadGlFunc("glShaderSource")
	glCompileShader = loadGlFunc("glCompileShader")
	glGetShaderiv = loadGlFunc("glGetShaderiv")
	glGenVertexArrays = loadGlFunc("glGenVertexArrays")
	glBindVertexArray = loadGlFunc("glBindVertexArray")
	glGenBuffers = loadGlFunc("glGenBuffers")
	glBindBuffer = loadGlFunc("glBindBuffer")
	glDeleteBuffers = loadGlFunc("glDeleteBuffers")
	glBufferData = loadGlFunc("glBufferData")
	glEnableVertexAttribArray = loadGlFunc("glEnableVertexAttribArray")
	glDisableVertexAttribArray = loadGlFunc("glDisableVertexAttribArray")
	glVertexAttribPointer = loadGlFunc("glVertexAttribPointer")
	glDeleteVertexArrays = loadGlFunc("glDeleteVertexArrays")
	glPixelStorei = loadGlFunc("glPixelStorei")
	glGenerateMipmap = loadGlFunc("glGenerateMipmap")
	glUniform1i = loadGlFunc("glUniform1i")
	glUniform1fv = loadGlFunc("glUniform1fv")
	glUniform2fv = loadGlFunc("glUniform2fv")
	glUniform4fv = loadGlFunc("glUniform4fv")
	glEnable = loadGlFunc("glEnable")
	glDisable = loadGlFunc("glDisable")
	glColorMask = loadGlFunc("glColorMask")
	glStencilMask = loadGlFunc("glStencilMask")
	glStencilFunc = loadGlFunc("glStencilFunc")
	glStencilOp = loadGlFunc("glStencilOp")
	glStencilOpSeparate = loadGlFunc("glStencilOpSeparate")
	glDrawArrays = loadGlFunc("glDrawArrays")
	glCullFace = loadGlFunc("glCullFace")
	glFrontFace = loadGlFunc("glFrontFace")
	glFinish = loadGlFunc("glFinish")
	glViewport = loadGlFunc("glViewport")
	glClear = loadGlFunc("glClear")
	glClearColor = loadGlFunc("glClearColor")
	glClearBufferfv = loadGlFunc("glClearBufferfv")

	return nil
}

func loadGlFunc(name string) (symbol cgo.Symbol) {
	fn, err := load(name)
	if err != nil {
		panic(fmt.Errorf("gl: load %s err: %v", name, err))
	}
	if fn == 0 {
		panic(fmt.Errorf("gl: can not load %s", name))
	}
	return cgo.Symbol(fn)
}

// Preserve pointer conversions at this outer entry point, before forwarding
// uintptr arguments to the FFI library. Requires its matching CallRaw contract.
//
//go:uintptrescapes
func call(fn cgo.Symbol, args ...uintptr) (ret uintptr) {
	ret, _, _ = fn.CallRaw(args...)
	return
}
