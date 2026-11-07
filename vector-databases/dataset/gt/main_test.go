package main

import (
	"math/rand"
	"sort"
	"testing"
)

// topK совпадает с полной сортировкой, включая порядок при равных скорах.
func TestTopKMatchesSort(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	corpus := make([][]float32, 2000)
	for i := range corpus {
		v := make([]float32, dim)
		for j := range v {
			v[j] = float32(r.Intn(3)) // много равных скоров
		}
		corpus[i] = v
	}
	q := corpus[7]
	got := topK(q, corpus, 50)
	idx := make([]int, len(corpus))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return dot(q, corpus[idx[a]]) > dot(q, corpus[idx[b]]) })
	for i, h := range got {
		if int(h.row) != idx[i] {
			t.Fatalf("позиция %d: %d, ожидался %d", i, h.row, idx[i])
		}
	}
}
