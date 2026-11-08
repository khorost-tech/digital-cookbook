// vbench — замеры стенда vector-databases/pgvector. Каждая подкоманда печатает
// одну строку JSON. Истина — точный перебор стенда dataset (без фильтра) и
// точный перебор по срезу корпуса, посчитанный здесь же (с фильтром).
//
//	vbench load                          таблица docs: 522 931 строка, vector(384) и halfvec(384)
//	vbench exact                         точный перебор в SQL (индексы выключены) против истины
//	vbench index -kind hnsw -m 16 -efc 200 -mwm 2GB | -kind ivfflat -lists 723 | -kind btree | -kind halfvec | -kind binary | -kind none
//	vbench search -param hnsw.ef_search -values 10,20,…
//	vbench ops                           <->, <=>, <#> при индексе vector_ip_ops: план и recall
//	vbench filter -sel 500,100,10,1 -modes off,relaxed_order,strict_order
//	vbench halfvec -values 40,160
//	vbench binary -values 100,400,1000
//	vbench hamming -values 10,100,1000    точный перебор по Хэммингу (без индекса): знак и знак после центрирования
//	vbench dump -ef 40                   выдача 200 запросов — для сверки с Java
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"

	"vdb/pgvector/vec"
)

const k = 10

var (
	dataDir = flag.String("data", "/data/quora", "корпус из стенда dataset")
	dsn     = flag.String("dsn", "postgres://postgres:pg@pg:5432/vdb", "PostgreSQL")
	nq      = flag.Int("q", 1000, "запросов из истины")
	ctx     = context.Background()
	notices []string
	reuse   = flag.Bool("reuse", false, "не перестраивать HNSW с тем же определением")
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func connect() *pgx.Conn {
	cfg, err := pgx.ParseConfig(*dsn)
	must(err)
	cfg.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) { notices = append(notices, n.Message) }
	c, err := pgx.ConnectConfig(ctx, cfg)
	must(err)
	// типы vector и halfvec регистрируются по OID — расширение должно уже быть
	_, err = c.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector")
	must(err)
	must(pgxvec.RegisterTypes(ctx, c))
	return c
}

func exec(c *pgx.Conn, sql string, args ...any) {
	_, err := c.Exec(ctx, sql, args...)
	must(err)
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

func ids(c *pgx.Conn, sql string, args ...any) []int32 {
	rows, err := c.Query(ctx, sql, args...)
	must(err)
	var out []int32
	for rows.Next() {
		var id int32
		must(rows.Scan(&id))
		out = append(out, id)
	}
	must(rows.Err())
	return out
}

// plan — узлы плана EXPLAIN одной строкой: «Limit → Index Scan using docs_hnsw on docs».
func plan(c *pgx.Conn, sql string, args ...any) string {
	var lines []string
	if len(args) == 0 && strings.Contains(sql, "$1") {
		// параметры без значений — общий план (PostgreSQL 16+); текст уходит
		// серверу как есть, мимо подстановки параметров pgx
		res, err := c.PgConn().Exec(ctx, "EXPLAIN (COSTS OFF, GENERIC_PLAN) "+sql).ReadAll()
		must(err)
		for _, row := range res[0].Rows {
			lines = append(lines, string(row[0]))
		}
	} else {
		rows, err := c.Query(ctx, "EXPLAIN (COSTS OFF) "+sql, args...)
		must(err)
		for rows.Next() {
			var line string
			must(rows.Scan(&line))
			lines = append(lines, line)
		}
		must(rows.Err())
	}
	var parts []string
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "->"))
		if strings.HasPrefix(line, "Order By") || strings.HasPrefix(line, "Filter") ||
			strings.HasPrefix(line, "Sort Key") || strings.HasPrefix(line, "Index Cond") ||
			strings.HasPrefix(line, "Recheck Cond") || strings.HasPrefix(line, "Workers") {
			continue
		}
		parts = append(parts, strings.TrimSpace(line))
	}
	return strings.Join(parts, " → ")
}

type point struct {
	Param  int     `json:"param"`
	Recall float64 `json:"recall_at_10"`
	Rows   float64 `json:"rows_per_query"`
	P50us  float64 `json:"p50_us"`
	P99us  float64 `json:"p99_us"`
}

// measure — запросы по одному, последовательно, на одном соединении. Сначала
// все запросы серии проходят один раз без замера: индекс (около 1 ГБ) больше
// shared_buffers, и первая серия после построения иначе мерила бы чтение с диска.
func measure(param int, queries []int, truth [][]int32, run func(i int) []int32) point {
	for i := range queries {
		run(i)
	}
	lat := make([]float64, len(queries))
	var rec, rows float64
	for i := range queries {
		t := time.Now()
		got := run(i)
		lat[i] = float64(time.Since(t).Microseconds())
		rows += float64(len(got))
		rec += vec.Recall(got, truth[i], k)
	}
	sort.Float64s(lat)
	n := float64(len(lat))
	return point{param, round(rec/n, 4), round(rows/n, 2), lat[int(0.5*n)], lat[min(len(lat)-1, int(0.99*n))]}
}

func relSize(c *pgx.Conn, name string) int64 {
	var b int64
	must(c.QueryRow(ctx, "SELECT pg_relation_size($1::regclass)", name).Scan(&b))
	return b
}

const searchSQL = "SELECT id FROM docs ORDER BY embedding <#> $1 LIMIT 10"

// meanLiteral — средний вектор корпуса как литерал '[…]'::vector. Детерминирован:
// сумма в float64 по порядку строк. Один и тот же текст идёт в выражение индекса
// и в запрос — иначе PostgreSQL не узнает выражение и пойдёт мимо индекса.
func meanLiteral(corpus [][]float32) string {
	sum := make([]float64, vec.Dim)
	for _, v := range corpus {
		for i, x := range v {
			sum[i] += float64(x)
		}
	}
	parts := make([]string, vec.Dim)
	for i, s := range sum {
		parts[i] = strconv.FormatFloat(s/float64(len(corpus)), 'g', 6, 32)
	}
	return "'[" + strings.Join(parts, ",") + "]'::vector"
}

// signBits — знаковый код вектора (бит = координата > сдвига), как binary_quantize.
func signBits(v, shift []float32) [6]uint64 {
	var b [6]uint64
	for i, x := range v {
		if x-shift[i] > 0 {
			b[i/64] |= 1 << (i % 64)
		}
	}
	return b
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "vbench load|exact|index|search|ops|filter|halfvec|binary [флаги]")
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.CommandLine
	kind := fs.String("kind", "hnsw", "индекс")
	m := fs.Int("m", 16, "m (hnsw)")
	efc := fs.Int("efc", 200, "ef_construction (hnsw)")
	mwm := fs.String("mwm", "2GB", "maintenance_work_mem")
	lists := fs.Int("lists", 723, "lists (ivfflat)")
	param := fs.String("param", "hnsw.ef_search", "параметр поиска")
	values := fs.String("values", "10,20,40,80,160,400", "значения параметра")
	sel := fs.String("sel", "500,100,10,1", "фильтр bucket < b, b из 1000")
	modes := fs.String("modes", "off,relaxed_order,strict_order", "hnsw.iterative_scan")
	ef := fs.Int("ef", 40, "hnsw.ef_search для filter")
	centered := fs.Bool("centered", false, "binary: коды от векторов минус средний вектор корпуса")
	planCache := fs.String("plancache", "", "plan_cache_mode для filter (auto, force_custom_plan, force_generic_plan)")
	must(fs.Parse(os.Args[2:]))

	c := connect()
	defer c.Close(ctx)
	switch cmd {
	case "load":
		loadTable(c)
	case "exact":
		exact(c, load(false))
	case "index":
		index(c, *kind, *m, *efc, *mwm, *lists)
	case "search":
		s := load(false)
		var pts []point
		for _, v := range ints(*values) {
			exec(c, fmt.Sprintf("SET %s = %d", *param, v))
			pts = append(pts, measure(v, s.gt.Rows, s.gt.IDs, func(i int) []int32 {
				return ids(c, searchSQL, pgvector.NewVector(s.queries[s.gt.Rows[i]]))
			}))
		}
		emit(map[string]any{"cmd": "search", "param": *param, "plan": plan(c, searchSQL, pgvector.NewVector(s.queries[0])),
			"points": pts})
	case "ops":
		ops(c, load(false))
	case "dump":
		// выдача первых 200 запросов — для сверки с Java
		s := load(false)
		exec(c, fmt.Sprintf("SET hnsw.ef_search = %d", *ef))
		var out [][]int32
		for _, r := range s.gt.Rows[:200] {
			out = append(out, ids(c, searchSQL, pgvector.NewVector(s.queries[r])))
		}
		emit(map[string]any{"cmd": "dump", "ef_search": *ef, "ids": out})
	case "filter":
		if *planCache != "" {
			exec(c, "SET plan_cache_mode = "+*planCache)
		}
		filter(c, load(true), ints(*sel), strings.Split(*modes, ","), *ef)
	case "halfvec":
		s := load(false)
		q := "SELECT id FROM docs ORDER BY embedding_h <#> $1 LIMIT 10"
		var pts []point
		for _, v := range ints(*values) {
			exec(c, fmt.Sprintf("SET hnsw.ef_search = %d", v))
			pts = append(pts, measure(v, s.gt.Rows, s.gt.IDs, func(i int) []int32 {
				return ids(c, q, pgvector.NewHalfVector(s.queries[s.gt.Rows[i]]))
			}))
		}
		emit(map[string]any{"cmd": "halfvec", "plan": plan(c, q, pgvector.NewHalfVector(s.queries[0])), "points": pts})
	case "binary":
		s := load(*centered)
		// кандидаты — по расстоянию Хэмминга между битовыми кодами (знак координаты),
		// затем пересчёт по исходным векторам; ширина обхода = числу кандидатов
		shift := ""
		if *centered {
			shift = " - " + meanLiteral(s.corpus)
		}
		q := `SELECT id FROM (SELECT id, embedding FROM docs
		        ORDER BY binary_quantize(embedding` + shift + `)::bit(384) <~> binary_quantize($1::vector` + shift + `) LIMIT $2) t
		      ORDER BY embedding <#> $1 LIMIT 10`
		var pts []point
		for _, v := range ints(*values) {
			exec(c, fmt.Sprintf("SET hnsw.ef_search = %d", v))
			pts = append(pts, measure(v, s.gt.Rows, s.gt.IDs, func(i int) []int32 {
				return ids(c, q, pgvector.NewVector(s.queries[s.gt.Rows[i]]), v)
			}))
		}
		emit(map[string]any{"cmd": "binary", "centered": *centered, "plan": plan(c, q, pgvector.NewVector(s.queries[0]), 100),
			"points": pts})
	case "hamming":
		hamming(load(true), ints(*values))
	default:
		fmt.Fprintln(os.Stderr, "неизвестная команда", cmd)
		os.Exit(2)
	}
}

func loadTable(c *pgx.Conn) {
	corpus, err := vec.ReadF32(filepath.Join(*dataDir, "corpus.f32"), 0)
	must(err)
	var have int
	if c.QueryRow(ctx, "SELECT count(*) FROM docs").Scan(&have) == nil && have == len(corpus) {
		emit(map[string]any{"cmd": "load", "rows": have, "loaded": false})
		return
	}
	exec(c, "DROP TABLE IF EXISTS docs")
	exec(c, `CREATE TABLE docs (id int PRIMARY KEY, bucket int NOT NULL,
	                            embedding vector(384) NOT NULL, embedding_h halfvec(384) NOT NULL)`)
	t := time.Now()
	n, err := c.CopyFrom(ctx, pgx.Identifier{"docs"}, []string{"id", "bucket", "embedding", "embedding_h"},
		pgx.CopyFromSlice(len(corpus), func(i int) ([]any, error) {
			return []any{int32(i), int32(vec.Bucket(i)), pgvector.NewVector(corpus[i]), pgvector.NewHalfVector(corpus[i])}, nil
		}))
	must(err)
	exec(c, "VACUUM ANALYZE docs")
	emit(map[string]any{"cmd": "load", "rows": n, "loaded": true, "load_s": round(time.Since(t).Seconds(), 1),
		"table_bytes": relSize(c, "docs")})
}

// exact — точный перебор в SQL. Индексы выключены настройкой сессии; при равных
// скорах — меньший id, как в истине. Расхождение с истиной допустимо только на
// почти равных скорах на границе десятки: pgvector складывает float32 в своём
// порядке.
func exact(c *pgx.Conn, s set) {
	exec(c, "SET enable_indexscan = off")
	exec(c, "SET enable_bitmapscan = off")
	q := "SELECT id FROM docs ORDER BY embedding <#> $1, id LIMIT 10"
	qs := s.gt.Rows[:200]
	agree, nearTie := 0, 0
	lat := make([]float64, len(qs))
	for i, r := range qs {
		t := time.Now()
		got := ids(c, q, pgvector.NewVector(s.queries[r]))
		lat[i] = float64(time.Since(t).Microseconds())
		if vec.Recall(got, s.gt.IDs[i], k) == 1 {
			agree++
		} else if s.scores[i][k-1]-s.scores[i][k] < 1e-5 {
			nearTie++
		}
	}
	sort.Float64s(lat)
	emit(map[string]any{"cmd": "exact", "queries": len(qs), "gt_agree": agree, "near_tie": nearTie,
		"p50_us": lat[len(lat)/2], "plan": plan(c, q, pgvector.NewVector(s.queries[0]))})
}

var noticeRx = regexp.MustCompile(`after (\d+) tuples`)

// index оставляет на таблице ровно один индекс по векторам (и B-tree для kind
// btree). С -reuse уже построенный HNSW с тем же определением не перестраивается.
func index(c *pgx.Conn, kind string, m, efc int, mwm string, lists int) {
	keep := ""
	if *reuse && (kind == "hnsw" || kind == "btree") {
		var def string
		want := fmt.Sprintf("WITH (m='%d', ef_construction='%d')", m, efc)
		if c.QueryRow(ctx, "SELECT indexdef FROM pg_indexes WHERE indexname = 'docs_hnsw'").Scan(&def) == nil &&
			strings.Contains(def, want) {
			keep = "docs_hnsw"
		}
	}
	for _, name := range []string{"docs_hnsw", "docs_ivf", "docs_bucket", "docs_half", "docs_bin"} {
		if name != keep {
			exec(c, "DROP INDEX IF EXISTS "+name)
		}
	}
	if keep != "" && kind == "hnsw" {
		emit(map[string]any{"cmd": "index", "kind": kind, "reused": true, "index_bytes": relSize(c, keep)})
		return
	}
	exec(c, "SET maintenance_work_mem = '"+mwm+"'")
	var name, sql string
	switch kind {
	case "none":
		emit(map[string]any{"cmd": "index", "kind": "none"})
		return
	case "hnsw":
		name = "docs_hnsw"
		sql = fmt.Sprintf("CREATE INDEX docs_hnsw ON docs USING hnsw (embedding vector_ip_ops) WITH (m = %d, ef_construction = %d)", m, efc)
	case "ivfflat":
		name = "docs_ivf"
		sql = fmt.Sprintf("CREATE INDEX docs_ivf ON docs USING ivfflat (embedding vector_ip_ops) WITH (lists = %d)", lists)
	case "btree":
		// HNSW остаётся, и рядом — B-tree по метке: планировщик выбирает между ними
		if keep == "" {
			must(fmt.Errorf("kind btree: сначала постройте HNSW (index -kind hnsw), затем -kind btree -reuse"))
		}
		name = "docs_bucket"
		sql = "CREATE INDEX docs_bucket ON docs (bucket)"
	case "halfvec":
		name = "docs_half"
		sql = fmt.Sprintf("CREATE INDEX docs_half ON docs USING hnsw (embedding_h halfvec_ip_ops) WITH (m = %d, ef_construction = %d)", m, efc)
	case "binary-centered":
		corpus, err := vec.ReadF32(filepath.Join(*dataDir, "corpus.f32"), 0)
		must(err)
		name = "docs_bin"
		sql = fmt.Sprintf("CREATE INDEX docs_bin ON docs USING hnsw ((binary_quantize(embedding - %s)::bit(384)) bit_hamming_ops) WITH (m = %d, ef_construction = %d)", meanLiteral(corpus), m, efc)
	case "binary":
		name = "docs_bin"
		sql = fmt.Sprintf("CREATE INDEX docs_bin ON docs USING hnsw ((binary_quantize(embedding)::bit(384)) bit_hamming_ops) WITH (m = %d, ef_construction = %d)", m, efc)
	default:
		must(fmt.Errorf("индекс %q", kind))
	}
	notices = nil
	t := time.Now()
	exec(c, sql)
	build := time.Since(t)
	exec(c, "ANALYZE docs")
	out := map[string]any{"cmd": "index", "kind": kind, "sql": sql, "maintenance_work_mem": mwm,
		"build_s": round(build.Seconds(), 1), "index_bytes": relSize(c, name), "table_bytes": relSize(c, "docs")}
	for _, n := range notices {
		if mm := noticeRx.FindStringSubmatch(n); mm != nil {
			out["graph_spilled_after"], _ = strconv.Atoi(mm[1])
			out["notice"] = n
		}
	}
	emit(out)
}

// ops — один индекс vector_ip_ops и три оператора: индекс работает только с
// оператором своего класса, остальные запросы идут перебором.
func ops(c *pgx.Conn, s set) {
	exec(c, "SET hnsw.ef_search = 40")
	qs := s.gt.Rows[:100]
	truth := s.gt.IDs[:100]
	out := map[string]any{"cmd": "ops"}
	for _, op := range []struct{ name, sym string }{{"l2", "<->"}, {"cosine", "<=>"}, {"ip", "<#>"}} {
		q := "SELECT id FROM docs ORDER BY embedding " + op.sym + " $1 LIMIT 10"
		p := measure(0, qs, truth, func(i int) []int32 { return ids(c, q, pgvector.NewVector(s.queries[qs[i]])) })
		out[op.name] = map[string]any{"operator": op.sym, "plan": plan(c, q, pgvector.NewVector(s.queries[0])),
			"recall_at_10": p.Recall, "p50_us": p.P50us}
	}
	emit(out)
}

// filter — WHERE bucket < b при ANN-индексе. Истина — точный перебор по срезу.
func filter(c *pgx.Conn, s set, sels []int, modes []string, ef int) {
	queries := s.gt.Rows[:200]
	exec(c, fmt.Sprintf("SET hnsw.ef_search = %d", ef))
	q := "SELECT id FROM docs WHERE bucket < $2 ORDER BY embedding <#> $1 LIMIT 10"
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
		for _, mode := range modes {
			exec(c, "SET hnsw.iterative_scan = "+mode)
			p := measure(b, queries, truth, func(i int) []int32 {
				return ids(c, q, pgvector.NewVector(s.queries[queries[i]]), b)
			})
			res = append(res, map[string]any{"bucket_lt": b, "selectivity": float64(b) / 1000, "rows_in_filter": len(sub),
				"mode": mode, "recall_at_10": p.Recall, "rows_per_query": p.Rows, "p50_us": p.P50us, "p99_us": p.P99us,
				"plan": plan(c, q, pgvector.NewVector(s.queries[0]), b)})
		}
	}
	exec(c, "SET hnsw.iterative_scan = off")
	var mode string
	must(c.QueryRow(ctx, "SHOW plan_cache_mode").Scan(&mode))
	// общий план подготовленного запроса — тот, что PostgreSQL берёт после пяти
	// выполнений, если он не дороже частных; значение b в нём неизвестно
	generic := plan(c, q)
	emit(map[string]any{"cmd": "filter", "ef_search": ef, "plan_cache_mode": mode, "generic_plan": generic, "points": res})
}

// hamming — точный перебор по расстоянию Хэмминга между знаковыми кодами, без
// графа: сколько теряет сам код. Плюс перекос знаков — у скольких координат
// один и тот же знак у 90% документов и больше.
func hamming(s set, rs []int) {
	zero := make([]float32, vec.Dim)
	mean := make([]float32, vec.Dim)
	sum := make([]float64, vec.Dim)
	for _, v := range s.corpus {
		for i, x := range v {
			sum[i] += float64(x)
		}
	}
	for i := range mean {
		mean[i] = float32(sum[i] / float64(len(s.corpus)))
	}
	out := map[string]any{"cmd": "hamming", "queries": 200}
	for _, mode := range []struct {
		name  string
		shift []float32
	}{{"sign", zero}, {"centered", mean}} {
		codes := make([][6]uint64, len(s.corpus))
		pos := make([]int, vec.Dim)
		for i, v := range s.corpus {
			codes[i] = signBits(v, mode.shift)
			for d := range pos {
				if codes[i][d/64]&(1<<(d%64)) != 0 {
					pos[d]++
				}
			}
		}
		skewed := 0
		for _, p := range pos {
			if f := float64(p) / float64(len(s.corpus)); f >= 0.9 || f <= 0.1 {
				skewed++
			}
		}
		rec := map[string]float64{}
		for _, r := range rs {
			var sum float64
			for qi, row := range s.gt.Rows[:200] {
				qc := signBits(s.queries[row], mode.shift)
				type h struct{ i, d int }
				hs := make([]h, len(codes))
				for i, x := range codes {
					d := 0
					for j := range x {
						d += bits.OnesCount64(x[j] ^ qc[j])
					}
					hs[i] = h{i, d}
				}
				sort.SliceStable(hs, func(a, b int) bool { return hs[a].d < hs[b].d })
				cand := hs[:r]
				q := s.queries[row]
				sort.SliceStable(cand, func(a, b int) bool { return vec.Dot(q, s.corpus[cand[a].i]) > vec.Dot(q, s.corpus[cand[b].i]) })
				got := make([]int32, 0, k)
				for _, x := range cand[:k] {
					got = append(got, int32(x.i))
				}
				sum += vec.Recall(got, s.gt.IDs[qi], k)
			}
			rec[strconv.Itoa(r)] = round(sum/200, 4)
		}
		out[mode.name] = map[string]any{"skewed_dims": skewed, "recall_at_10": rec}
	}
	emit(out)
}
