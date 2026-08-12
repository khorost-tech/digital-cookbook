//go:build amd64

package dotprod

import (
	"fmt"
	"testing"
)

var sink float64

func benchInputs(n int) (a, b []float64) {
	a = make([]float64, n)
	b = make([]float64, n)
	for i := range a {
		a[i] = float64(i%97) * 0.5
		b[i] = float64(i%89) * 0.25
	}
	return
}

func BenchmarkDotGeneric(b *testing.B) {
	for _, n := range []int{8, 256, 1024, 1 << 16} {
		x, y := benchInputs(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.SetBytes(int64(n * 16))
			for b.Loop() {
				sink = dotGeneric(x, y)
			}
		})
	}
}

func BenchmarkDotAVX2(b *testing.B) {
	for _, n := range []int{8, 256, 1024, 1 << 16} {
		x, y := benchInputs(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.SetBytes(int64(n * 16))
			for b.Loop() {
				sink = dotAVX2(&x[0], &y[0], len(x))
			}
		})
	}
}
