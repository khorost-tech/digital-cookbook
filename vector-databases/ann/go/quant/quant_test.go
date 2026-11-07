package quant

import (
	"math"
	"math/rand"
	"testing"

	"vdb/ann/vec"
)

func normed(n, d int, seed int64) [][]float32 {
	r := rand.New(rand.NewSource(seed))
	out := make([][]float32, n)
	for i := range out {
		v := make([]float32, d)
		var s float64
		for j := range v {
			v[j] = float32(r.NormFloat64())
			s += float64(v[j] * v[j])
		}
		for j := range v {
			v[j] /= float32(math.Sqrt(s))
		}
		out[i] = v
	}
	return out
}

// Ошибка SQ8 по координате — не больше половины шага.
func TestSQ8ErrorWithinHalfStep(t *testing.T) {
	data := normed(1000, 32, 1)
	q := TrainSQ8(data)
	for i := 0; i < 100; i++ {
		dec := q.Decode(i)
		for j, x := range data[i] {
			if e := math.Abs(float64(x - dec[j])); e > float64(q.Scale[j])/2+1e-6 {
				t.Fatalf("точка %d, координата %d: ошибка %g, шаг %g", i, j, e, q.Scale[j])
			}
		}
	}
	if len(q.Codes) != 1000*32 {
		t.Fatalf("кодов %d байт", len(q.Codes))
	}
}

// Размер кода PQ — N·m байт; пересчёт всех кандидатов возвращает точную выдачу.
func TestPQCodeSizeAndFullRescore(t *testing.T) {
	data := normed(2000, 32, 2)
	p := TrainPQ(data, data, 8, 5, 2)
	if len(p.Codes) != 2000*8 {
		t.Fatalf("кодов %d байт, ожидалось %d", len(p.Codes), 2000*8)
	}
	q := data[5]
	got := p.Search(q, 10, len(data), data, true)
	if r := vec.Recall(got, vec.Exact(q, data, 10, vec.CosineDist), 10); r != 1 {
		t.Fatalf("r = N с пересчётом: recall %.2f", r)
	}
}
