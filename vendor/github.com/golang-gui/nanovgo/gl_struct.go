package nanovgo

import (
	"github.com/golang-gui/nanovgo/gl"
)

const (
	nsvgShaderFILLGRAD = iota
	nsvgShaderFILLIMG
	nsvgShaderSIMPLE
	nsvgShaderIMG
	nsvgShaderBOXSHADOW
)

type glnvgCallType int

const (
	glnvgNONE glnvgCallType = iota
	glnvgFILL
	glnvgCONVEXFILL
	glnvgSTROKE
	glnvgTRIANGLES
	glnvgTRIANGLESTRIP
)

type glCall struct {
	callType       glnvgCallType
	composite      CompositeOperation
	image          int
	pathOffset     int
	pathCount      int
	triangleOffset int
	triangleCount  int
	uniformOffset  int
	coverageOffset int
	coverageCount  int
	coveragePixel  float32
}

type glPath struct {
	fillOffset   int
	fillCount    int
	strokeOffset int
	strokeCount  int
}

type glFragUniforms [44]float32

func (u *glFragUniforms) reset() {
	for i := 0; i < 44; i++ {
		u[i] = 0
	}
}

func (u *glFragUniforms) setScissorMat(mat []float32) {
	copy(u[0:12], mat[0:12])
}

func (u *glFragUniforms) clearScissorMat() {
	for i := 0; i < 12; i++ {
		u[i] = 0
	}
}

func (u *glFragUniforms) setPaintMat(mat []float32) {
	copy(u[12:24], mat[0:12])
}

func (u *glFragUniforms) setInnerColor(color Color) {
	copy(u[24:28], color.List())
}

func (u *glFragUniforms) setOuterColor(color Color) {
	copy(u[28:32], color.List())
}

// Shadows use a single color, so their otherwise unused outer-color uniform
// stores corner geometry. Ordinary paints retain the existing uniform layout.
func (u *glFragUniforms) setShadowCorners(corners [4]float32) {
	copy(u[28:32], corners[:])
}

func (u *glFragUniforms) setScissorExt(a, b float32) {
	u[32] = a
	u[33] = b
}

func (u *glFragUniforms) setScissorScale(a, b float32) {
	u[34] = a
	u[35] = b
}

func (u *glFragUniforms) setExtent(ext [2]float32) {
	copy(u[36:38], ext[:])
}

func (u *glFragUniforms) setRadius(radius float32) {
	u[38] = radius
}

func (u *glFragUniforms) setFeather(feather float32) {
	u[39] = feather
}

func (u *glFragUniforms) setStrokeMult(strokeMult float32) {
	u[40] = strokeMult
}

func (u *glFragUniforms) setStrokeThr(strokeThr float32) {
	u[41] = strokeThr
}

func (u *glFragUniforms) setTexType(texType float32) {
	u[42] = texType
}

func (u *glFragUniforms) setType(typeCode float32) {
	u[43] = typeCode
}

type glTexture struct {
	id            int
	tex           gl.Texture
	width, height int
	texType       nvgTextureType
	flags         ImageFlags
}
