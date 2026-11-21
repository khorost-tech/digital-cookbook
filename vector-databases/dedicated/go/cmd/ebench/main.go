// ebench — замеры стенда vector-databases/dedicated: Qdrant, Milvus и Weaviate
// на корпусе стенда dataset. Каждая подкоманда печатает одну строку JSON.
//
//	ebench load   -engine qdrant [-frac 0.9] [-quant int8] [-filter-strategy sweeping]   пересоздать коллекцию, загрузить, дождаться индекса
//	ebench search -engine qdrant -values 10,20,…            recall@10 и время от ef
//	ebench exact  -engine qdrant                             точный поиск движка против истины (Qdrant)
//	ebench fresh  -engine qdrant [-strong]                   догрузить оставшиеся документы и сразу искать их
//	ebench filter -engine qdrant -sel 500,100,10,1           WHERE bucket < b против точного перебора по срезу
//	ebench quant  -engine qdrant -values 40                  int8 без пересчёта и с пересчётом
//	ebench dump   -engine qdrant -ef 40                      выдача 200 запросов — для сверки с Java
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"vdb/dedicated/vec"
)

const k = 10

var (
	dataDir = flag.String("data", "/data/quora", "корпус из стенда dataset")
	nq      = flag.Int("q", 1000, "запросов из истины")
	ctx     = context.Background()
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func emit(v any) {
	b, err := json.Marshal(v)
	must(err)
	fmt.Println(string(b))
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

func round(x float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.Round(x*p) / p
}

func engine(name string) Engine {
	switch name {
	case "qdrant":
		return newQdrant()
	case "milvus":
		return newMilvus()
	case "weaviate":
		return newWeaviate()
	}
	must(fmt.Errorf("движок %q", name))
	return nil
}

type set struct {
	corpus, queries [][]float32
	gt              *vec.GT
	scores          [][]float32
}

func load(withCorpus bool) set {
	var s set
	var err error
	if withCorpus {
		s.corpus, err = vec.ReadF32(filepath.Join(*dataDir, "corpus.f32"), 0)
		must(err)
	}
	s.queries, err = vec.ReadF32(filepath.Join(*dataDir, "queries.f32"), 0)
	must(err)
	s.gt, err = vec.ReadGT(*dataDir, 100)
	must(err)
	s.scores, err = vec.ReadGTScores(*dataDir, len(s.gt.Rows), 100)
	must(err)
	if *nq < len(s.gt.Rows) {
		s.gt.Rows, s.gt.IDs, s.scores = s.gt.Rows[:*nq], s.gt.IDs[:*nq], s.scores[:*nq]
	}
	return s
}

type point struct {
	Param  int     `json:"param"`
	Recall float64 `json:"recall_at_10"`
	Rows   float64 `json:"rows_per_query"`
	P50us  float64 `json:"p50_us"`
	P99us  float64 `json:"p99_us"`
}

// measure — запросы по одному, последовательно; сначала серия проходит один раз
// без замера (прогрев кешей движка).
func measure(param int, n int, truth [][]int32, run func(i int) []int32) point {
	for i := 0; i < n; i++ {
		run(i)
	}
	lat := make([]float64, n)
	var rec, rows float64
	for i := 0; i < n; i++ {
		t := time.Now()
		got := run(i)
		lat[i] = float64(time.Since(t).Microseconds())
		rows += float64(len(got))
		rec += vec.Recall(got, truth[i], k)
	}
	sort.Float64s(lat)
	f := float64(n)
	return point{param, round(rec/f, 4), round(rows/f, 2), lat[int(0.5*f)], lat[min(n-1, int(0.99*f))]}
}

func search(e Engine, q []float32, p Params) []int32 {
	ids, err := e.Search(ctx, q, k, p)
	must(err)
	return ids
}

// insertAll — вставка строк [from, to) корпуса пачками по batch.
func insertAll(e Engine, corpus [][]float32, from, to, batch int) {
	for i := from; i < to; i += batch {
		j := min(i+batch, to)
		ids := make([]int, j-i)
		for x := range ids {
			ids[x] = i + x
		}
		must(e.Insert(ctx, ids, corpus[i:j]))
	}
}

func batchSize(name string) int {
	if name == "milvus" {
		return 10000
	}
	return 1000
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "ebench load|search|exact|fresh|filter|quant|dump [флаги]")
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.CommandLine
	name := fs.String("engine", "qdrant", "qdrant | milvus | weaviate")
	frac := fs.Float64("frac", 1, "доля корпуса для load")
	quant := fs.String("quant", "", "int8 — скалярная квантизация (Qdrant)")
	filterStrategy := fs.String("filter-strategy", "", "Weaviate: sweeping вместо acorn по умолчанию")
	m := fs.Int("m", 16, "M")
	efc := fs.Int("efc", 200, "ef_construction")
	values := fs.String("values", "10,20,40,80,160,400", "ef")
	sel := fs.String("sel", "500,100,10,1", "фильтр bucket < b, b из 1000")
	ef := fs.Int("ef", 40, "ef для fresh, filter, dump")
	strong := fs.Bool("strong", false, "Milvus: Strong-согласованность в fresh")
	must(fs.Parse(os.Args[2:]))
	e := engine(*name)

	switch cmd {
	case "load":
		s := load(true)
		n := int(float64(len(s.corpus)) * *frac)
		_ = e.Reset(ctx)
		must(e.Create(ctx, Config{M: *m, EfC: *efc, Quant: *quant, Filter: *filterStrategy}))
		t := time.Now()
		insertAll(e, s.corpus, 0, n, batchSize(*name))
		inserted := time.Since(t)
		must(e.Ready(ctx))
		ready := time.Since(t)
		info, err := e.Info(ctx)
		must(err)
		emit(map[string]any{"cmd": "load", "engine": *name, "rows": n, "m": *m, "efc": *efc, "quant": *quant, "filter_strategy": *filterStrategy,
			"insert_s": round(inserted.Seconds(), 1), "ready_s": round(ready.Seconds(), 1), "info": info})
	case "search":
		s := load(false)
		var pts []point
		for _, v := range ints(*values) {
			p := Params{Ef: v, BucketLt: -1}
			must(e.Prepare(ctx, p))
			pts = append(pts, measure(v, len(s.gt.Rows), s.gt.IDs, func(i int) []int32 {
				return search(e, s.queries[s.gt.Rows[i]], p)
			}))
		}
		emit(map[string]any{"cmd": "search", "engine": *name, "points": pts})
	case "exact":
		s := load(false)
		agree, nearTie := 0, 0
		qs := s.gt.Rows[:200]
		for i, r := range qs {
			got := search(e, s.queries[r], Params{Ef: 40, BucketLt: -1, Exact: true})
			if vec.Recall(got, s.gt.IDs[i], k) == 1 {
				agree++
			} else if s.scores[i][k-1]-s.scores[i][k] < 1e-5 {
				nearTie++
			}
		}
		emit(map[string]any{"cmd": "exact", "engine": *name, "queries": len(qs), "gt_agree": agree, "near_tie": nearTie})
	case "fresh":
		fresh(e, *name, *ef, *strong)
	case "filter":
		filter(e, *name, load(true), ints(*sel), *ef)
	case "quant":
		s := load(false)
		var res []map[string]any
		for _, v := range ints(*values) {
			for _, mode := range []struct {
				name    string
				rescore bool
				over    float64
			}{{"codes", false, 1}, {"rescore", true, 2}} {
				p := Params{Ef: v, BucketLt: -1, Rescore: mode.rescore, Oversampling: mode.over}
				pt := measure(v, len(s.gt.Rows), s.gt.IDs, func(i int) []int32 { return search(e, s.queries[s.gt.Rows[i]], p) })
				res = append(res, map[string]any{"ef": v, "mode": mode.name, "oversampling": mode.over,
					"recall_at_10": pt.Recall, "p50_us": pt.P50us, "p99_us": pt.P99us})
			}
		}
		emit(map[string]any{"cmd": "quant", "engine": *name, "points": res})
	case "dump":
		s := load(false)
		p := Params{Ef: *ef, BucketLt: -1}
		must(e.Prepare(ctx, p))
		var out [][]int32
		for _, r := range s.gt.Rows[:200] {
			out = append(out, search(e, s.queries[r], p))
		}
		emit(map[string]any{"cmd": "dump", "engine": *name, "ef": *ef, "ids": out})
	default:
		fmt.Fprintln(os.Stderr, "неизвестная команда", cmd)
		os.Exit(2)
	}
}

// fresh — коллекция уже загружена на 90% (load -frac 0.9) и проиндексирована.
// Догружаем оставшиеся 10% и сразу после ответа на последнюю вставку ищем
// каждым 50-м новым документом по самому себе: нашёлся ли он в top-10 и
// сколько занял запрос. Затем ждём Ready и повторяем.
func fresh(e Engine, name string, ef int, strong bool) {
	corpus, err := vec.ReadF32(filepath.Join(*dataDir, "corpus.f32"), 0)
	must(err)
	from := int(float64(len(corpus)) * 0.9)
	p := Params{Ef: ef, BucketLt: -1, Strong: strong}
	must(e.Prepare(ctx, p))
	var probe []int
	for i := from; i < len(corpus); i += 50 {
		probe = append(probe, i)
	}
	self := func() map[string]any {
		found := 0
		lat := make([]float64, len(probe))
		for i, id := range probe {
			t := time.Now()
			got := search(e, corpus[id], p)
			lat[i] = float64(time.Since(t).Microseconds())
			for _, g := range got {
				if int(g) == id {
					found++
					break
				}
			}
		}
		sort.Float64s(lat)
		return map[string]any{"found": found, "probes": len(probe), "p50_us": lat[len(lat)/2]}
	}
	t := time.Now()
	insertAll(e, corpus, from, len(corpus), batchSize(name))
	inserted := time.Since(t)
	before, err := e.Info(ctx)
	must(err)
	now := self()
	t = time.Now()
	must(e.Ready(ctx))
	ready := time.Since(t)
	after := self()
	info, err := e.Info(ctx)
	must(err)
	emit(map[string]any{"cmd": "fresh", "engine": name, "strong": strong, "inserted": len(corpus) - from,
		"insert_s": round(inserted.Seconds(), 1), "info_before": before, "at_once": now,
		"ready_s": round(ready.Seconds(), 1), "after_ready": after, "info_after": info})
}

// filter — WHERE bucket < b; истина — точный перебор по срезу.
func filter(e Engine, name string, s set, sels []int, ef int) {
	queries := s.gt.Rows[:200]
	var res []map[string]any
	for _, b := range sels {
		var sub [][]float32
		var subIDs []int32
		for i, v := range s.corpus {
			if vec.Bucket(i) < b {
				sub = append(sub, v)
				subIDs = append(subIDs, int32(i))
			}
		}
		truth := make([][]int32, len(queries))
		for i, r := range queries {
			local := vec.Exact(s.queries[r], sub, k, vec.DotDist)
			for j := range local {
				local[j] = subIDs[local[j]]
			}
			truth[i] = local
		}
		p := Params{Ef: ef, BucketLt: b}
		must(e.Prepare(ctx, p))
		pt := measure(b, len(queries), truth, func(i int) []int32 { return search(e, s.queries[queries[i]], p) })
		res = append(res, map[string]any{"bucket_lt": b, "selectivity": float64(b) / 1000, "rows_in_filter": len(sub),
			"recall_at_10": pt.Recall, "rows_per_query": pt.Rows, "p50_us": pt.P50us, "p99_us": pt.P99us})
	}
	emit(map[string]any{"cmd": "filter", "engine": name, "ef": ef, "points": res})
}
