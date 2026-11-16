package hnsw

import "math"

type DistanceFunc func(a, b []float32) float32

// CosineDistance uses portable Go on every supported OS/architecture. Source
// validation rejects zero, non-finite, and mismatched vectors before indexing.
func CosineDistance(a, b []float32) float32 {
	var dot, aa, bb float64
	for index, value := range a {
		x, y := float64(value), float64(b[index])
		dot += x * y
		aa += x * x
		bb += y * y
	}
	if aa == 0 || bb == 0 {
		return 1
	}
	return float32(1 - max(-1, min(1, dot/math.Sqrt(aa*bb))))
}
