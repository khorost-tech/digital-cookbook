package dotprod

import (
	"math"
	"math/rand"
	"testing"
)

// randVec — детерминированные псевдослучайные векторы (seed фиксирован).
func randVec(r *rand.Rand, n int) []float64 {
	v := make([]float64, n)
	for i := range v {
		v[i] = r.NormFloat64()
	}
	return v
}

// closeEnough — относительный допуск: FMA и переупорядочивание сумм в SIMD
// дают чуть другое округление, чем последовательный generic-цикл.
func closeEnough(got, want float64) bool {
	return math.Abs(got-want) <= 1e-9*(1+math.Abs(want))
}

func TestDotMatchesGeneric(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for _, n := range []int{0, 1, 2, 3, 4, 7, 8, 15, 16, 31, 100, 1000} {
		a, b := randVec(r, n), randVec(r, n)
		got := Dot(a, b)
		want := dotGeneric(a, b)
		if !closeEnough(got, want) {
			t.Fatalf("n=%d: Dot=%v dotGeneric=%v", n, got, want)
		}
	}
}

func TestDotLengthMismatchPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("ожидалась паника при разной длине")
		}
	}()
	Dot([]float64{1, 2}, []float64{1})
}
