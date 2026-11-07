package ivf

import (
	"math/rand"
	"testing"

	"vdb/ann/vec"
)

func random(n, d int, seed int64) [][]float32 {
	r := rand.New(rand.NewSource(seed))
	out := make([][]float32, n)
	for i := range out {
		out[i] = make([]float32, d)
		for j := range out[i] {
			out[i][j] = float32(r.NormFloat64())
		}
	}
	return out
}

// nprobe = nlist — просмотрен весь корпус, выдача совпадает с точным перебором.
func TestAllListsIsExact(t *testing.T) {
	data := random(3000, 16, 1)
	ix := Build(data, data[:1000], 32, 10, 1, vec.L2)
	for q := 0; q < 50; q++ {
		got, scanned := ix.Search(data[q*7], 10, 32)
		if scanned != len(data) {
			t.Fatalf("просмотрено %d из %d", scanned, len(data))
		}
		if r := vec.Recall(got, vec.Exact(data[q*7], data, 10, vec.L2), 10); r != 1 {
			t.Fatalf("recall %.2f", r)
		}
	}
}

// Каждая точка лежит ровно в одном списке, и это список ближайшего центроида.
func TestListsPartition(t *testing.T) {
	data := random(2000, 8, 2)
	ix := Build(data, data, 16, 5, 2, vec.L2)
	seen := make([]int, len(data))
	for c, l := range ix.Lists {
		for _, i := range l {
			seen[i]++
			if nearest(data[i], ix.Centroids) != c {
				t.Fatalf("точка %d в списке %d, ближайший центроид другой", i, c)
			}
		}
	}
	for i, s := range seen {
		if s != 1 {
			t.Fatalf("точка %d в %d списках", i, s)
		}
	}
}
