package ivf

import (
	"sort"

	"vdb/ann/vec"
)

// Index — IVF-Flat: в списках лежат исходные векторы, без сжатия.
type Index struct {
	Centroids [][]float32
	Lists     [][]int32
	data      [][]float32
	dist      func(a, b []float32) float32
}

// Build — центроиды обучаются на train, затем в списки раскладывается весь data.
func Build(data, train [][]float32, nlist, iters int, seed int64, dist func(a, b []float32) float32) *Index {
	ix := &Index{Centroids: KMeans(train, nlist, iters, seed), data: data, dist: dist}
	assign := make([]int, len(data))
	Assign(data, ix.Centroids, assign)
	ix.Lists = make([][]int32, nlist)
	for i, a := range assign {
		ix.Lists[a] = append(ix.Lists[a], int32(i))
	}
	return ix
}

type scored struct {
	i int32
	d float32
}

// Search — k ближайших среди nprobe ближайших к запросу списков; второе
// значение — сколько векторов просмотрено.
func (ix *Index) Search(q []float32, k, nprobe int) ([]int32, int) {
	cs := make([]scored, len(ix.Centroids))
	for c, v := range ix.Centroids {
		cs[c] = scored{int32(c), vec.L2(q, v)}
	}
	sort.Slice(cs, func(a, b int) bool { return cs[a].d < cs[b].d })
	var best []scored
	scanned := 0
	for _, c := range cs[:min(nprobe, len(cs))] {
		for _, i := range ix.Lists[c.i] {
			scanned++
			d := ix.dist(q, ix.data[i])
			if len(best) == k && d >= best[k-1].d {
				continue
			}
			j := len(best)
			for j > 0 && best[j-1].d > d {
				j--
			}
			best = append(best, scored{})
			copy(best[j+1:], best[j:])
			best[j] = scored{i, d}
			if len(best) > k {
				best = best[:k]
			}
		}
	}
	out := make([]int32, len(best))
	for i, b := range best {
		out[i] = b.i
	}
	return out, scanned
}

// ListSizes — длины списков: перекос показывает, насколько выборка обучения
// не похожа на данные.
func (ix *Index) ListSizes() []int {
	out := make([]int, len(ix.Lists))
	for i, l := range ix.Lists {
		out[i] = len(l)
	}
	return out
}
