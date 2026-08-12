//go:build arm64

package dotprod

//go:noescape
func dotNEON(a, b *float64, n int) float64

// Dot на arm64 использует NEON (128-бит, 2 float64 за такт). NEON есть в
// базовом ARMv8, отдельный детект фич не нужен.
func Dot(a, b []float64) float64 {
	if len(a) != len(b) {
		panic("dotprod: length mismatch")
	}
	if len(a) == 0 {
		return 0
	}
	return dotNEON(&a[0], &b[0], len(a))
}
