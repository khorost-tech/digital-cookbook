// bench — замеры стенда vector-databases/ann. Каждый подкоманд печатает одну
// строку JSON: параметры, recall@10 против истины точного перебора, время на
// запрос (один поток, p50/p99) и число посчитанных расстояний — меру работы,
// которая от железа не зависит.
//
//	bench brute  -n 10000,50000,…
//	bench hnsw   -m 16 -efc 200 -ef 10,20,…
//	bench ivf    -nlist 724 -nprobe 1,2,… [-train sample|skewed]
//	bench sq8    -r 10,100
//	bench pq     -pqm 48 -r 10,100
//	bench metric
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"vdb/ann/hnsw"
	"vdb/ann/ivf"
	"vdb/ann/quant"
	"vdb/ann/vec"
)

const k = 10

var (
	dataDir = flag.String("data", "/data/quora", "корпус из стенда dataset")
	nq      = flag.Int("q", 1000, "запросов из истины")
	seed    = flag.Int64("seed", 1, "seed")
)

type set struct {
	corpus, queries [][]float32
	gt              *vec.GT
}

func load() set {
	c, err := vec.ReadF32(filepath.Join(*dataDir, "corpus.f32"), 0)
	must(err)
	q, err := vec.ReadF32(filepath.Join(*dataDir, "queries.f32"), 0)
	must(err)
	g, err := vec.ReadGT(*dataDir, 100)
	must(err)
	if *nq < len(g.Rows) {
		g.Rows, g.IDs = g.Rows[:*nq], g.IDs[:*nq]
	}
	return set{c, q, g}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func ints(s string) []int {
	var out []int
	for _, f := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		must(err)
		out = append(out, n)
	}
	return out
}

// point — один замер: recall и стоимость на одном значении ручки.
type point struct {
	Param  int     `json:"param"`
	Recall float64 `json:"recall_at_10"`
	P50us  float64 `json:"p50_us"`
	P99us  float64 `json:"p99_us"`
	Dists  float64 `json:"dists_per_query"`
}

// measure прогоняет все запросы одним потоком; search возвращает выдачу и
// число посчитанных расстояний.
func measure(s set, param int, search func(q []float32) ([]int32, int)) point {
	lat := make([]float64, len(s.gt.Rows))
	var rec, dists float64
	for i, r := range s.gt.Rows {
		t := time.Now()
		got, d := search(s.queries[r])
		lat[i] = float64(time.Since(t).Microseconds())
		rec += vec.Recall(got, s.gt.IDs[i], k)
		dists += float64(d)
	}
	sort.Float64s(lat)
	n := float64(len(lat))
	return point{param, round(rec/n, 4), lat[int(0.5*n)], lat[min(len(lat)-1, int(0.99*n))], round(dists/n, 1)}
}

func round(x float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.Round(x*p) / p
}

func heap() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func emit(v any) {
	b, err := json.Marshal(v)
	must(err)
	fmt.Println(string(b))
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "bench brute|hnsw|ivf|sq8|pq|metric [флаги]")
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.CommandLine
	ns := fs.String("n", "", "размеры корпуса (brute)")
	m := fs.Int("m", 16, "M (hnsw)")
	efc := fs.Int("efc", 200, "ef_construction (hnsw)")
	efs := fs.String("ef", "10,20,40,80,160,400", "ef_search (hnsw)")
	workers := fs.Int("workers", 0, "потоков построения, 0 — все ядра")
	nlist := fs.Int("nlist", 724, "списков (ivf)")
	nprobes := fs.String("nprobe", "1,2,4,8,16,32,64", "nprobe (ivf)")
	train := fs.String("train", "sample", "выборка обучения: sample — каждый 10-й, skewed — 10% ближайших к одной точке")
	rs := fs.String("r", "10,100,1000", "кандидатов до пересчёта (sq8, pq)")
	pqm := fs.Int("pqm", 48, "подвекторов (pq)")
	must(fs.Parse(os.Args[2:]))

	s := load()
	switch cmd {
	case "brute":
		brute(s, ints(*ns))
	case "hnsw":
		h0 := heap()
		t := time.Now()
		g := hnsw.Build(s.corpus, *m, *efc, vec.DotDist, *seed, *workers)
		build := time.Since(t)
		h1 := heap()
		reach := g.Reachable()
		unreachable := 0
		for _, ok := range reach {
			if !ok {
				unreachable++
			}
		}
		var pts []point
		for _, ef := range ints(*efs) {
			pts = append(pts, measure(s, ef, func(q []float32) ([]int32, int) { return g.Search(q, k, ef) }))
		}
		emit(map[string]any{"algo": "hnsw", "n": len(s.corpus), "m": *m, "efc": *efc, "workers": *workers,
			"build_s": round(build.Seconds(), 1), "link_bytes": g.LinkBytes(), "heap_bytes": h1 - h0,
			"vector_bytes": int64(len(s.corpus)) * vec.Dim * 4, "levels": g.Levels(), "unreachable": unreachable,
			"points": pts})
	case "ivf":
		tr := trainSet(s.corpus, *train)
		t := time.Now()
		ix := ivf.Build(s.corpus, tr, *nlist, 20, *seed, vec.DotDist)
		build := time.Since(t)
		sizes := ix.ListSizes()
		sort.Ints(sizes)
		var pts []point
		for _, p := range ints(*nprobes) {
			pts = append(pts, measure(s, p, func(q []float32) ([]int32, int) {
				got, scanned := ix.Search(q, k, p)
				return got, scanned + *nlist
			}))
		}
		emit(map[string]any{"algo": "ivf-flat", "n": len(s.corpus), "nlist": *nlist, "train": *train,
			"train_n": len(tr), "build_s": round(build.Seconds(), 1), "list_min": sizes[0],
			"list_median": sizes[len(sizes)/2], "list_max": sizes[len(sizes)-1],
			"empty_lists": countZero(sizes), "points": pts})
	case "sq8":
		t := time.Now()
		q := quant.TrainSQ8(s.corpus)
		build := time.Since(t)
		var pts []map[string]any
		for _, r := range ints(*rs) {
			for _, ex := range []bool{false, true} {
				p := measure(s, r, func(qv []float32) ([]int32, int) { return q.Search(qv, k, r, s.corpus, ex), len(s.corpus) })
				pts = append(pts, map[string]any{"r": r, "rescore": ex, "recall_at_10": p.Recall, "p50_us": p.P50us,
					"p99_us": p.P99us})
			}
		}
		emit(map[string]any{"algo": "sq8", "n": len(s.corpus), "code_bytes": len(q.Codes),
			"build_s": round(build.Seconds(), 1), "points": pts})
	case "pq":
		tr := trainSet(s.corpus, "sample")
		t := time.Now()
		p := quant.TrainPQ(tr, s.corpus, *pqm, 20, *seed)
		build := time.Since(t)
		var pts []map[string]any
		for _, r := range ints(*rs) {
			for _, ex := range []bool{false, true} {
				pt := measure(s, r, func(qv []float32) ([]int32, int) { return p.Search(qv, k, r, s.corpus, ex), len(s.corpus) })
				pts = append(pts, map[string]any{"r": r, "rescore": ex, "recall_at_10": pt.Recall, "p50_us": pt.P50us,
					"p99_us": pt.P99us})
			}
		}
		emit(map[string]any{"algo": "pq", "n": len(s.corpus), "pq_m": *pqm, "sub": p.Sub, "code_bytes": len(p.Codes),
			"codebook_bytes": p.CodebookBytes(), "train_n": len(tr), "build_s": round(build.Seconds(), 1),
			"points": pts})
	case "metric":
		metric(s)
	default:
		fmt.Fprintln(os.Stderr, "неизвестная команда", cmd)
		os.Exit(2)
	}
}

func countZero(xs []int) int {
	n := 0
	for _, x := range xs {
		if x == 0 {
			n++
		}
	}
	return n
}

// trainSet: sample — каждый 10-й вектор; skewed — 10% корпуса, ближайшие к
// одному случайному документу, то есть одна тема.
func trainSet(c [][]float32, kind string) [][]float32 {
	n := len(c) / 10
	switch kind {
	case "sample":
		out := make([][]float32, 0, n)
		for i := 0; i < len(c); i += 10 {
			out = append(out, c[i])
		}
		return out
	case "skewed":
		anchor := c[rand.New(rand.NewSource(*seed)).Intn(len(c))]
		ids := vec.Exact(anchor, c, n, vec.DotDist)
		out := make([][]float32, len(ids))
		for i, id := range ids {
			out[i] = c[id]
		}
		return out
	}
	must(fmt.Errorf("выборка %q", kind))
	return nil
}

// brute — точный перебор на первых n документах. Время на запрос растёт
// линейно; на полном корпусе выдача обязана совпасть с истиной датасета.
func brute(s set, ns []int) {
	queries := s.gt.Rows
	if len(queries) > 200 {
		queries = queries[:200]
	}
	var pts []map[string]any
	for _, n := range ns {
		n = min(n, len(s.corpus))
		data := s.corpus[:n]
		lat := make([]float64, len(queries))
		agree := 0
		for i, r := range queries {
			t := time.Now()
			got := vec.Exact(s.queries[r], data, k, vec.DotDist)
			lat[i] = float64(time.Since(t).Microseconds())
			if n == len(s.corpus) && vec.Recall(got, s.gt.IDs[i], k) == 1 {
				agree++
			}
		}
		sort.Float64s(lat)
		pt := map[string]any{"n": n, "p50_us": lat[len(lat)/2], "dists_per_query": n}
		if n == len(s.corpus) {
			pt["gt_agree"] = agree
			pt["queries"] = len(queries)
		}
		pts = append(pts, pt)
	}
	emit(map[string]any{"algo": "brute", "points": pts})
}

// metric — согласие top-10 по трём метрикам на нормированных векторах и на тех
// же векторах, умноженных на случайную длину от 0,5 до 2.
func metric(s set) {
	r := rand.New(rand.NewSource(*seed))
	n := min(100000, len(s.corpus))
	norm := s.corpus[:n]
	scaled := make([][]float32, n)
	for i, v := range norm {
		f := float32(0.5 + 1.5*r.Float64())
		w := make([]float32, len(v))
		for j, x := range v {
			w[j] = x * f
		}
		scaled[i] = w
	}
	// overlap — средняя доля общих документов в top-10 двух метрик; same —
	// у скольких запросов top-10 совпал как множество
	agree := func(data [][]float32, a, b func(x, y []float32) float32) map[string]float64 {
		var ov float64
		same := 0
		for _, row := range s.gt.Rows[:200] {
			q := s.queries[row]
			r := vec.Recall(vec.Exact(q, data, k, a), vec.Exact(q, data, k, b), k)
			ov += r
			if r == 1 {
				same++
			}
		}
		return map[string]float64{"overlap": round(ov/200, 4), "same": float64(same)}
	}
	emit(map[string]any{"algo": "metric", "n": n, "queries": 200,
		"normalized": map[string]any{"cosine_vs_l2": agree(norm, vec.CosineFullDist, vec.L2),
			"cosine_vs_dot": agree(norm, vec.CosineFullDist, vec.DotDist)},
		"scaled": map[string]any{"cosine_vs_l2": agree(scaled, vec.CosineFullDist, vec.L2),
			"cosine_vs_dot": agree(scaled, vec.CosineFullDist, vec.DotDist)}})
}
