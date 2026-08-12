//go:build amd64

package dotprod

import "golang.org/x/sys/cpu"

//go:noescape
func dotAVX2(a, b *float64, n int) float64

// Dot использует AVX2+FMA, если процессор их поддерживает, иначе — generic.
func Dot(a, b []float64) float64 {
	if len(a) != len(b) {
		panic("dotprod: length mismatch")
	}
	if len(a) == 0 {
		return 0
	}
	if cpu.X86.HasAVX2 && cpu.X86.HasFMA {
		return dotAVX2(&a[0], &b[0], len(a))
	}
	return dotGeneric(a, b)
}
