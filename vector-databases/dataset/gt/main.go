// Истина для recall: точный перебор (скалярное произведение нормированных
// векторов = косинус) по всему корпусу, top-K соседей для подвыборки запросов.
//
//	gt <корпус> [запросов] [K]   → <корпус>/gt.rows, gt.i32, gt.f32
//
// gt.rows — номера выбранных запросов (строки queries.f32), по одному в строке:
// каждый шаг-й, шаг = всего / запросов. gt.i32 — номера строк корпуса, gt.f32 —
// их скоры, по K на запрос, от ближайшего.
package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"
)

const dim = 384

func readF32(path string) [][]float32 {
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	n := len(b) / 4 / dim
	out := make([][]float32, n)
	flat := make([]float32, n*dim)
	for i := range flat {
		flat[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	for i := range out {
		out[i] = flat[i*dim : (i+1)*dim]
	}
	return out
}

// dot — ровно та же сумма, что vec.Dot стенда ann (развёртка на 4): иначе
// порядок сложения разный, и соседи с почти равным скором меняются местами.
func dot(a, b []float32) float32 {
	var s0, s1, s2, s3 float32
	n := len(a) &^ 3
	for i := 0; i < n; i += 4 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
	}
	for i := n; i < len(a); i++ {
		s0 += a[i] * b[i]
	}
	return s0 + s1 + s2 + s3
}

type hit struct {
	row   int32
	score float32
}

// topK — точный перебор с порядком (скор по убыванию, при равенстве — номер строки).
func topK(q []float32, corpus [][]float32, k int) []hit {
	h := make([]hit, 0, k+1)
	for i, v := range corpus {
		s := dot(q, v)
		if len(h) == k && s <= h[k-1].score {
			continue
		}
		j := sort.Search(len(h), func(j int) bool { return h[j].score < s })
		h = append(h, hit{})
		copy(h[j+1:], h[j:])
		h[j] = hit{int32(i), s}
		if len(h) > k {
			h = h[:k]
		}
	}
	return h
}

func main() {
	root := os.Args[1]
	want, k := 1000, 100
	if len(os.Args) > 2 {
		want, _ = strconv.Atoi(os.Args[2])
	}
	if len(os.Args) > 3 {
		k, _ = strconv.Atoi(os.Args[3])
	}
	corpus := readF32(filepath.Join(root, "corpus.f32"))
	queries := readF32(filepath.Join(root, "queries.f32"))
	step := 1
	if len(queries) > want {
		step = len(queries) / want
	}
	var rows []int
	for i := 0; i < len(queries) && len(rows) < want; i += step {
		rows = append(rows, i)
	}
	res := make([][]hit, len(rows))
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				res[i] = topK(queries[rows[i]], corpus, k)
			}
		}()
	}
	for i := range rows {
		next <- i
	}
	close(next)
	wg.Wait()

	fr, _ := os.Create(filepath.Join(root, "gt.rows"))
	wr := bufio.NewWriter(fr)
	fi, _ := os.Create(filepath.Join(root, "gt.i32"))
	wi := bufio.NewWriter(fi)
	ff, _ := os.Create(filepath.Join(root, "gt.f32"))
	wf := bufio.NewWriter(ff)
	for i, r := range rows {
		fmt.Fprintln(wr, r)
		for _, h := range res[i] {
			binary.Write(wi, binary.LittleEndian, h.row)
			binary.Write(wf, binary.LittleEndian, h.score)
		}
	}
	for _, w := range []*bufio.Writer{wr, wi, wf} {
		w.Flush()
	}
	fmt.Printf("{\"corpus\": %q, \"docs\": %d, \"queries\": %d, \"k\": %d}\n", root, len(corpus), len(rows), k)
}
