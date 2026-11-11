package gl

import (
	"runtime"
	"unsafe"

	"github.com/goexlib/cgo"
)

type LoadFunc func(symbol string) (fn uintptr, err error)

var initialized bool

func Init(loadFn LoadFunc) (err error) {
	if initialized {
		return nil
	}

	err = loadGlFuncs(loadFn)
	if err != nil {
		return
	}

	initialized = true
	return nil
}

func Initialized() bool {
	return initialized
}

func GetError() Enum {
	ret := call(glGetError)
	return Enum(ret)
}

func BindTexture(target Enum, texture Texture) {
	call(glBindTexture, uintptr(target), uintptr(texture.Value))
}

func CreateTexture() (texture Texture) {
	//glGenTextures(GLsizei n, GLuint *textures)
	call(glGenTextures, 1, uintptr(cgo.Pointer(&texture)))
	return
}

func DeleteTexture(texture Texture) {
	//glDeleteTextures(GLsizei n, const GLuint *textures)
	call(glDeleteTextures, 1, uintptr(cgo.Pointer(&texture)))
}

func GenTextures(n Sizei) (textures []Texture) {
	//glGenTextures(GLsizei n, GLuint *textures)
	textures = make([]Texture, n)
	call(glGenTextures, uintptr(len(textures)), uintptr(cgo.CSlice(textures)))
	runtime.KeepAlive(textures)
	return
}

func DeleteTextures(textures []Texture) {
	//glDeleteTextures(GLsizei n, const GLuint *textures)
	call(glDeleteTextures, uintptr(len(textures)), uintptr(cgo.CSlice(textures)))
}

func ActiveTexture(texture Enum) {
	call(glActiveTexture, uintptr(texture))
}

func TexImage2D(target Enum, level int, width, height int, format Enum, ty Enum, data []byte) {
	// glTexImage2D(GLenum target, GLint level, GLint internalformat, GLsizei width, GLsizei height, GLint border, GLenum format, GLenum type, const void *pixels);
	call(glTexImage2D, uintptr(target), uintptr(level), uintptr(format), uintptr(width), uintptr(height), 0, uintptr(format), uintptr(ty), uintptr(cgo.CSlice(data)))
}

func TexSubImage2D(target Enum, level int, x, y, width, height int, format, ty Enum, data []byte) {
	// glTexSubImage2D(GLenum target, GLint level, GLint xoffset, GLint yoffset, GLsizei width, GLsizei height, GLenum format, GLenum type, const void *pixels);
	call(glTexSubImage2D, uintptr(target), uintptr(level), uintptr(x), uintptr(y), uintptr(width), uintptr(height), uintptr(format), uintptr(ty), uintptr(cgo.CSlice(data)))
}

func TexParameteri(target Enum, name Enum, param Int) {
	call(glTexParameteri, uintptr(target), uintptr(name), uintptr(param))
}

func BlendFunc(sfactor, dfactor Enum) {
	call(glBlendFunc, uintptr(sfactor), uintptr(dfactor))
}

func BlendFuncSeparate(sFactorRgb, dFactorRgb, sFactorAlpha, dFactorAlpha Enum) {
	call(glBlendFuncSeparate, uintptr(sFactorRgb), uintptr(dFactorRgb), uintptr(sFactorAlpha), uintptr(dFactorAlpha))
}

func CreateProgram() Program {
	ret := call(glCreateProgram)
	return Program{uint32(ret)}
}

func DeleteProgram(program Program) {
	call(glDeleteProgram, uintptr(program.Value))
}

func GetProgrami(program Program, name Enum) (value Int) {
	//glGetProgramiv(GLuint program, GLenum pname, GLint *params)
	call(glGetProgramiv, uintptr(program.Value), uintptr(name), uintptr(unsafe.Pointer(&value)))
	return
}

func GetProgramInfoLog(program Program) string {
	//glGetProgramInfoLog(GLuint program, GLsizei bufSize, GLsizei *length, GLchar *infoLog)
	length := GetProgrami(program, INFO_LOG_LENGTH)
	if length != 0 {
		buf := make([]byte, length)
		call(glGetProgramInfoLog, uintptr(program.Value), uintptr(length), uintptr(unsafe.Pointer(&length)), uintptr(unsafe.Pointer(cgo.CSlice(buf))))
		return cgo.GoStringNTemp(cgo.CSlice(buf), int(length))
		runtime.KeepAlive(buf)
	}
	return ""
}

func AttachShader(program Program, shader Shader) {
	call(glAttachShader, uintptr(program.Value), uintptr(shader.Value))
}

func GetAttribLocation(p Program, name string) Attrib {
	cName := cgo.CStringTemp(name)
	ret := call(glGetAttribLocation, uintptr(p.Value), uintptr(cName))
	runtime.KeepAlive(cName)
	return Attrib{uint(ret)}
}

func BindAttribLocation(program Program, index Uint, name string) {
	//glBindAttribLocation(GLuint program, GLuint index, const GLchar *name)
	cName := cgo.CStringTemp(name)
	call(glBindAttribLocation, uintptr(program.Value), uintptr(index), uintptr(cName))
	runtime.KeepAlive(cName)
}

func LinkProgram(program Program) {
	call(glLinkProgram, uintptr(program.Value))
}

func UseProgram(program Program) {
	call(glUseProgram, uintptr(program.Value))
}

func GetUniformLocation(program Program, name string) Uniform {
	//glGetUniformLocation(GLuint program, const GLchar *name)
	cName := cgo.CStringTemp(name)
	ret := call(glGetUniformLocation, uintptr(program.Value), uintptr(cName))
	runtime.KeepAlive(cName)
	return Uniform{int32(ret)}
}

func CreateShader(shaderType Enum) Shader {
	ret := call(glCreateShader, uintptr(shaderType))
	return Shader{uint32(ret)}
}

func DeleteShader(shader Shader) {
	call(glDeleteShader, uintptr(shader.Value))
}

func GetShaderInfoLog(shader Shader) string {
	//glGetShaderInfoLog)(GLuint shader, GLsizei bufSize, GLsizei *length, GLchar *infoLog)
	length := GetShaderi(shader, INFO_LOG_LENGTH)
	if length != 0 {
		buf := make([]byte, length)
		call(glGetShaderInfoLog, uintptr(shader.Value), uintptr(length), 0, uintptr(unsafe.Pointer(cgo.CSlice(buf))))
		return cgo.GoStringNTemp(cgo.CSlice(buf), int(length))
		runtime.KeepAlive(buf)
	}
	return ""
}

func ShaderSource(shader Shader, sources ...string) {
	//glShaderSource(GLuint shader, GLsizei count, const GLchar *const*string, const GLint *length)
	cSources := make([]unsafe.Pointer, len(sources))
	for i := range sources {
		source := sources[i]
		if source == "" {
			// Each entry must point to a string, even when that string is empty.
			// CStringTemp("") is NULL, not a pointer to a terminating NUL.
			source = "\x00"
		}
		cSources[i] = cgo.CStringTemp(source)
	}
	call(glShaderSource, uintptr(shader.Value), uintptr(len(sources)), uintptr(cgo.CSlice(cSources)), 0)
	// glShaderSource copies the source strings. The pointer slice keeps all
	// temporary Go buffers alive until that synchronous call finishes.
	runtime.KeepAlive(cSources)
}

func CompileShader(shader Shader) {
	call(glCompileShader, uintptr(shader.Value))
}

func GetShaderi(shader Shader, name Enum) (value Int) {
	//glGetShaderiv(GLuint shader, GLenum pname, GLint *params)
	call(glGetShaderiv, uintptr(shader.Value), uintptr(name), uintptr(unsafe.Pointer(&value)))
	return
}

func GenVertexArray() (array Uint) {
	//glGenVertexArrays(GLsizei n, GLuint *arrays)
	call(glGenVertexArrays, 1, uintptr(cgo.Pointer(&array)))
	return
}

func DeleteVertexArray(array Uint) {
	//glDeleteVertexArrays(GLsizei n, const GLuint *arrays)
	call(glDeleteVertexArrays, 1, uintptr(cgo.Pointer(&array)))
}

func GenVertexArrays(n Sizei) (arrays []Uint) {
	//glGenVertexArrays(GLsizei n, GLuint *arrays)
	arrays = make([]Uint, n)
	call(glGenVertexArrays, uintptr(len(arrays)), uintptr(cgo.CSlice(arrays)))
	runtime.KeepAlive(arrays)
	return
}

func DeleteVertexArrays(arrays []Uint) {
	//glDeleteVertexArrays(GLsizei n, const GLuint *arrays)
	call(glDeleteVertexArrays, uintptr(len(arrays)), uintptr(cgo.CSlice(arrays)))
}

func BindVertexArray(array Uint) {
	call(glBindVertexArray, uintptr(array))
}

func EnableVertexAttribArray(index Attrib) {
	call(glEnableVertexAttribArray, uintptr(index.Value))
}

func DisableVertexAttribArray(index Attrib) {
	call(glDisableVertexAttribArray, uintptr(index.Value))
}

func VertexAttribPointer(index Attrib, size Int, typ Enum, normalized bool, stride Sizei, pointer uintptr) {
	call(glVertexAttribPointer, uintptr(index.Value), uintptr(size), uintptr(typ), uintptr(cgo.CBool(normalized)), uintptr(stride), pointer)
}

func CreateBuffer() (buffer Buffer) {
	//glGenBuffers(GLsizei n, GLuint *buffers)
	call(glGenBuffers, 1, uintptr(cgo.Pointer(&buffer)))
	return
}

func DeleteBuffer(buffer Buffer) {
	//glDeleteBuffers(GLsizei n, const GLuint *buffers)
	call(glDeleteBuffers, 1, uintptr(cgo.Pointer(&buffer)))
}

func GenBuffers(n Sizei) (buffers []Buffer) {
	//glGenBuffers(GLsizei n, GLuint *buffers)
	buffers = make([]Buffer, n)
	call(glGenBuffers, uintptr(len(buffers)), uintptr(cgo.CSlice(buffers)))
	runtime.KeepAlive(buffers)
	return
}

func DeleteBuffers(buffers []Buffer) {
	//glDeleteBuffers(GLsizei n, const GLuint *buffers)
	call(glDeleteBuffers, uintptr(len(buffers)), uintptr(cgo.CSlice(buffers)))
	runtime.KeepAlive(buffers)
}

func BindBuffer(target Enum, buffer Buffer) {
	call(glBindBuffer, uintptr(target), uintptr(buffer.Value))
}

func BufferData[T any](target Enum, data []T, usage Enum) {
	//void glBufferData(GLenum target, GLsizeiptr size, const void *data, GLenum usage)
	var zero T
	call(glBufferData, uintptr(target), uintptr(len(data))*unsafe.Sizeof(zero), uintptr(cgo.CSlice(data)), uintptr(usage))
	runtime.KeepAlive(data)
}

func PixelStorei(name Enum, param Int) {
	call(glPixelStorei, uintptr(name), uintptr(param))
}

func GenerateMipmap(target Enum) {
	call(glGenerateMipmap, uintptr(target))
}

func Uniform1i(location Uniform, value Int) {
	call(glUniform1i, uintptr(location.Value), uintptr(value))
}

func Uniform1fv(location Int, values []Float) {
	//glUniform1fv(GLint location, GLsizei count, const GLfloat *value)
	call(glUniform1fv, uintptr(location), uintptr(len(values)), uintptr(cgo.CSlice(values)))
	runtime.KeepAlive(values)
}

func Uniform2f(location Uniform, v0, v1 Float) {
	Uniform2fv(location, []Float{v0, v1})
}

func Uniform2fv(location Uniform, values []Float) {
	//glUniform2fv(GLint location, GLsizei count, const GLfloat *value)
	call(glUniform2fv, uintptr(location.Value), uintptr(len(values)/2), uintptr(cgo.CSlice(values)))
	runtime.KeepAlive(values)
}

func Uniform4f(location Uniform, v0, v1, v2, v3 Float) {
	Uniform4fv(location, []Float{v0, v1, v2, v3})
}

func Uniform4fv(location Uniform, values []Float) {
	//glUniform4fv(GLint location, GLsizei count, const GLfloat *value)
	call(glUniform4fv, uintptr(location.Value), uintptr(len(values)/4), uintptr(cgo.CSlice(values)))
	runtime.KeepAlive(values)
}

func Enable(cap Enum) {
	call(glEnable, uintptr(cap))
}

func Disable(cap Enum) {
	call(glDisable, uintptr(cap))
}

func ColorMask(red, green, blue, alpha bool) {
	call(glColorMask, uintptr(cgo.CBool(red)), uintptr(cgo.CBool(green)), uintptr(cgo.CBool(blue)), uintptr(cgo.CBool(alpha)))
}

func StencilMask(mask Uint) {
	call(glStencilMask, uintptr(mask))
}

func StencilFunc(fn Enum, ref int, mask Uint) {
	call(glStencilFunc, uintptr(fn), uintptr(ref), uintptr(mask))
}

func StencilOp(fail, zFail, zPass Enum) {
	call(glStencilOp, uintptr(fail), uintptr(zFail), uintptr(zPass))
}

func StencilOpSeparate(face, sFail, dpFail, dpPass Enum) {
	call(glStencilOpSeparate, uintptr(face), uintptr(sFail), uintptr(dpFail), uintptr(dpPass))
}

func DrawArrays(mode Enum, first int, count int) {
	call(glDrawArrays, uintptr(mode), uintptr(first), uintptr(count))
}

func CullFace(mode Enum) {
	call(glCullFace, uintptr(mode))
}

func FrontFace(mode Enum) {
	call(glFrontFace, uintptr(mode))
}

func Finish() {
	call(glFinish)
}

func Viewport(x, y, width, height int) {
	call(glViewport, uintptr(x), uintptr(y), uintptr(width), uintptr(height))
}

func Clear(mask Bitfield) {
	call(glClear, uintptr(mask))
}

func ClearColor(r, g, b, a Float) {
	cgo.Call(glClearColor, r, g, b, a)
}

func ClearBufferfv(buffer Enum, drawBuffer Int, values []Float) {
	//glClearBufferfv(GLenum buffer, GLint drawbuffer, const GLfloat *value)
	call(glClearBufferfv, uintptr(buffer), uintptr(drawBuffer), uintptr(cgo.CSlice(values)))
	runtime.KeepAlive(values)
}
