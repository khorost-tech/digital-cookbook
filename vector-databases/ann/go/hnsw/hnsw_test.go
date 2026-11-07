package hnsw

import (
	"math"
	"math/rand"
	"testing"

	"vdb/ann/vec"
)

func clustered(n, d, c int, seed int64) [][]float32 {
	r := rand.New(rand.NewSource(seed))
	cent := make([][]float32, c)
	for i := range cent {
		cent[i] = make([]float32, d)
		for j := range cent[i] {
			cent[i][j] = float32(r.NormFloat64())
		}
	}
	out := make([][]float32, n)
	for i := range out {
		v := make([]float32, d)
		cc := cent[r.Intn(c)]
		var s float64
		for j := range v {
			v[j] = cc[j] + float32(r.NormFloat64())*0.8
			s += float64(v[j] * v[j])
		}
		for j := range v {
			v[j] /= float32(math.Sqrt(s))
		}
		out[i] = v
	}
	return out
}

func recall(t *testing.T, g *Graph, data [][]float32, ef int) float64 {
	t.Helper()
	var s float64
	for q := 0; q < 100; q++ {
		qv := data[q*13]
		got, _ := g.Search(qv, 10, ef)
		s += vec.Recall(got, vec.Exact(qv, data, 10, vec.CosineDist), 10)
	}
	return s / 100
}

// На тех же данных, где coder/hnsw давал 0,17, своя реализация обязана держать
// высокий recall и расти с ef.
func TestRecallGrowsWithEf(t *testing.T) {
	data := clustered(20000, 384, 50, 1)
	g := Build(data, 16, 100, vec.CosineDist, 1, 0)
	r20, r200 := recall(t, g, data, 20), recall(t, g, data, 200)
	if r200 < 0.95 || r200 <= r20 {
		t.Fatalf("recall@10: ef=20 %.3f, ef=200 %.3f", r20, r200)
	}
}

// Ширина обхода не меньше числа узлов — поиск находит всех достижимых соседей;
// промахи — только узлы, до которых по рёбрам слоя 0 не дойти.
func TestEfAtLeastNMissesOnlyUnreachable(t *testing.T) {
	data := clustered(2000, 32, 10, 2)
	g := Build(data, 8, 50, vec.CosineDist, 2, 4)
	reach := g.Reachable()
	for q := 0; q < 100; q++ {
		qv := data[q*13]
		got, _ := g.Search(qv, 10, len(data))
		found := map[int32]bool{}
		for _, x := range got {
			found[x] = true
		}
		var want []int32
		for _, x := range vec.Exact(qv, data, len(data), vec.CosineDist) {
			if reach[x] {
				want = append(want, x)
			}
			if len(want) == 10 {
				break
			}
		}
		for _, x := range want {
			if !found[x] {
				t.Fatalf("запрос %d: достижимый сосед %d не найден при ef = N", q, x)
			}
		}
	}
}

func TestLevelsShrink(t *testing.T) {
	data := clustered(5000, 16, 5, 3)
	g := Build(data, 16, 32, vec.CosineDist, 3, 1)
	l := g.Levels()
	if l[0] != 5000 || len(l) < 2 || float64(l[1]) > 5000.0/16*1.5 {
		t.Fatalf("узлов по слоям: %v", l)
	}
}
