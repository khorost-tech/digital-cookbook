// Пакет ivf — инвертированные списки: пространство режется k-means на nlist
// ячеек, вектор лежит в списке ближайшего центроида, поиск смотрит nprobe
// ближайших к запросу списков.
package ivf

import (
	"math/rand"
	"runtime"
	"sync"

	"vdb/ann/vec"
)

// KMeans — алгоритм Ллойда: центроиды — k случайных точек выборки, затем iters
// раз «отнести каждую точку к ближайшему центроиду, пересчитать центроиды как
// средние». Пустой кластер получает случайную точку выборки.
func KMeans(data [][]float32, k, iters int, seed int64) [][]float32 {
	r := rand.New(rand.NewSource(seed))
	d := len(data[0])
	cent := make([][]float32, k)
	for i, p := range r.Perm(len(data))[:k] {
		cent[i] = append([]float32(nil), data[p]...)
	}
	assign := make([]int, len(data))
	for it := 0; it < iters; it++ {
		Assign(data, cent, assign)
		sum := make([][]float64, k)
		cnt := make([]int, k)
		for i := range sum {
			sum[i] = make([]float64, d)
		}
		for i, a := range assign {
			cnt[a]++
			for j, x := range data[i] {
				sum[a][j] += float64(x)
			}
		}
		for c := range cent {
			if cnt[c] == 0 {
				copy(cent[c], data[r.Intn(len(data))])
				continue
			}
			for j := range cent[c] {
				cent[c][j] = float32(sum[c][j] / float64(cnt[c]))
			}
		}
	}
	return cent
}

// Assign — номер ближайшего (по L2) центроида для каждой точки, параллельно.
func Assign(data, cent [][]float32, out []int) {
	var wg sync.WaitGroup
	w := runtime.NumCPU()
	for s := 0; s < w; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			for i := s; i < len(data); i += w {
				out[i] = nearest(data[i], cent)
			}
		}(s)
	}
	wg.Wait()
}

func nearest(x []float32, cent [][]float32) int {
	best, bd := 0, vec.L2(x, cent[0])
	for c := 1; c < len(cent); c++ {
		if d := vec.L2(x, cent[c]); d < bd {
			best, bd = c, d
		}
	}
	return best
}
