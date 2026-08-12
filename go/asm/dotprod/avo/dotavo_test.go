//go:build amd64

package avo

import (
	"math"
	"math/rand"
	"testing"
)

func TestDotAvoMatchesGeneric(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for _, n := range []int{0, 1, 3, 4, 7, 8, 16, 31, 100, 1000} {
		a := make([]float64, n)
		b := make([]float64, n)
		for i := range a {
			a[i] = r.NormFloat64()
			b[i] = r.NormFloat64()
		}
		got := DotAvo(a, b)
		want := dotAvoGeneric(a, b)
		if math.Abs(got-want) > 1e-9*(1+math.Abs(want)) {
			t.Fatalf("n=%d: DotAvo=%v generic=%v", n, got, want)
		}
	}
}
