package mathx

import (
	"math"
	"unsafe"
)

type Integer interface {
	~int8 | ~int16 | ~int32 | ~int64 | ~int | ~uint8 | ~uint16 | ~uint32 | ~uint64
}

type Float interface {
	~float32 | ~float64
}

type Numeric interface {
	Integer | Float
}

func IsNaN[T Float](x T) bool {
	return math.IsNaN(float64(x))
}

func IsInf[T Float, S Integer](x T, sign S) bool {
	return math.IsInf(float64(x), int(sign))
}

func NaN[T Numeric]() T {
	return T(math.NaN())
}

func Inf[T Numeric, S Integer](sign S) T {
	return T(math.Inf(int(sign)))
}

func Abs[T Numeric](x T) T {
	return T(math.Abs(float64(x)))
}

func Cos[T Numeric](x T) T {
	return T(math.Cos(float64(x)))
}

func Cosh[T Numeric](x T) T {
	return T(math.Cosh(float64(x)))
}

func Acos[T Numeric](x T) T {
	return T(math.Acos(float64(x)))
}

func Acosh[T Numeric](x T) T {
	return T(math.Acosh(float64(x)))
}

func Sin[T Numeric](x T) T {
	return T(math.Sin(float64(x)))
}

func Sinh[T Numeric](x T) T {

	return T(math.Sinh(float64(x)))
}

func Asin[T Numeric](x T) T {
	return T(math.Asin(float64(x)))
}

func Asinh[T Numeric](x T) T {
	return T(math.Asinh(float64(x)))
}

func SinCos[T Numeric](x T) (sin, cos T) {
	s, c := math.Sincos(float64(x))
	return T(s), T(c)
}

func Tan[T Numeric](x T) T {
	return T(math.Tan(float64(x)))
}

func Tanh[T Numeric](x T) T {
	return T(math.Tanh(float64(x)))
}

func Atan[T Numeric](x T) T {
	return T(math.Atan(float64(x)))
}

func Atanh[T Numeric](x T) T {
	return T(math.Atanh(float64(x)))
}

func Atan2[T Numeric](x, y T) T {
	return T(math.Atan2(float64(x), float64(y)))
}

func Sqrt[T Numeric](x T) T {
	return T(math.Sqrt(float64(x)))
}

func Cbrt[T Numeric](x T) T {
	return T(math.Cbrt(float64(x)))
}

func Dim[T Numeric](x, y T) T {
	return T(math.Dim(float64(x), float64(y)))
}

func Pow[T Numeric](x, y T) T {
	return T(math.Pow(float64(x), float64(y)))
}

func Log[T Numeric](x T) T {
	return T(math.Log(float64(x)))
}

func Log2[T Numeric](x T) T {
	return T(math.Log2(float64(x)))
}

func Log10[T Numeric](x T) T {
	return T(math.Log10(float64(x)))
}

func Logb[T Numeric](x T) T {
	return T(math.Logb(float64(x)))
}

func Ilogb[T Numeric](x T) int {
	return math.Ilogb(float64(x))
}

func Log1p[T Numeric](x T) T {
	return T(math.Log1p(float64(x)))
}

func Ceil[T Numeric](x T) T {
	return T(math.Ceil(float64(x)))
}

func Round[T Numeric](x T) T {
	return T(math.Round(float64(x)))
}

func RoundToEven[T Numeric](x T) T {
	return T(math.RoundToEven(float64(x)))
}

func Trunc[T Numeric](x T) T {
	return T(math.Trunc(float64(x)))
}

func Floor[T Numeric](x T) T {
	return T(math.Floor(float64(x)))
}

func Mod[T Numeric](x, y T) T {
	return T(math.Mod(float64(x), float64(y)))
}

func Modf[T Numeric](x T) (int, frac T) {
	i, f := math.Modf(float64(x))
	return T(i), T(f)
}

func Exp[T Numeric](x T) T {
	return T(math.Exp(float64(x)))
}

func Exp2[T Numeric](x T) T {
	return T(math.Exp2(float64(x)))
}

func Expm1[T Numeric](x T) T {
	return T(math.Expm1(float64(x)))
}

func Frexp[T Numeric](f T) (int, frac T) {
	i, fc := math.Frexp(float64(f))
	return T(i), T(fc)
}

func Ldexp[T Numeric, E Integer](frac T, exp E) T {
	return T(math.Ldexp(float64(frac), int(exp)))
}

func Erf[T Numeric](x T) T {
	return T(math.Erf(float64(x)))
}

func Erfc[T Numeric](x T) T {
	return T(math.Erfc(float64(x)))
}

func Erfinv[T Numeric](x T) T {
	return T(math.Erfinv(float64(x)))
}

func Erfcinv[T Numeric](x T) T {
	return T(math.Erfcinv(float64(x)))
}

func FMA[T Numeric](x, y, z T) T {
	return T(math.FMA(float64(x), float64(y), float64(z)))
}

func Gamma[T Numeric](x T) T {
	return T(math.Gamma(float64(x)))
}

func Lgamma[T Numeric](x T) (lgama T, sign int) {
	l, s := math.Lgamma(float64(x))
	return T(l), s
}

func Hypot[T Numeric](p, q T) T {
	return T(math.Hypot(float64(p), float64(q)))
}

func J0[T Numeric](x T) T {
	return T(math.J0(float64(x)))
}

func J1[T Numeric](x T) T {
	return T(math.J1(float64(x)))
}

func Jn[N Integer, T Numeric](n N, x T) T {
	return T(math.Jn(int(n), float64(x)))
}

func Y0[T Numeric](x T) T {
	return T(math.Y0(float64(x)))
}

func Y1[T Numeric](x T) T {
	return T(math.Y1(float64(x)))
}

func Yn[N Integer, T Numeric](n N, x T) T {
	return T(math.Yn(int(n), float64(x)))
}

func Copysign[F Numeric, S Numeric](f F, sign S) F {
	return F(math.Copysign(float64(f), float64(sign)))
}

func NextAfter[T Numeric](x, y T) T {
	if unsafe.Sizeof(x) <= 4 {
		return T(math.Nextafter32(float32(x), float32(y)))
	}
	return T(math.Nextafter(float64(x), float64(y)))
}

func Remainder[T Numeric](x, y T) T {
	return T(math.Remainder(float64(x), float64(y)))
}
