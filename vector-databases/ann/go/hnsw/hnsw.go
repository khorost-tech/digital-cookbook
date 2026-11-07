// Пакет hnsw — Hierarchical Navigable Small World (Malkov, Yashunin, 2016):
// многослойный граф соседства. Верхние слои редкие — длинные переходы, слой 0
// содержит все узлы — короткие. Поиск спускается жадно сверху и на слое 0
// обходит граф с очередью ширины ef.
package hnsw

import (
	"math"
	"math/rand"
	"runtime"
	"sort"
	"sync"
)

// Graph — индекс. M — число связей узла на верхних слоях, на слое 0 — 2·M;
// EfConstruction — ширина поиска соседей при вставке.
type Graph struct {
	M, EfConstruction int
	Dist              func(a, b []float32) float32

	vecs   [][]float32
	levels []int8
	links  [][][]int32 // узел → слой → соседи
	locks  []sync.Mutex

	mu       sync.RWMutex
	entry    int32
	maxLevel int
	visited  sync.Pool
}

// Build строит граф по всем векторам в workers потоках. Уровни узлов
// разыгрываются заранее генератором с seed, но порядок вставок между потоками
// не детерминирован: при workers > 1 граф от прогона к прогону немного разный.
func Build(vecs [][]float32, m, efc int, dist func(a, b []float32) float32, seed int64, workers int) *Graph {
	g := &Graph{M: m, EfConstruction: efc, Dist: dist, vecs: vecs, entry: -1}
	n := len(vecs)
	g.levels = make([]int8, n)
	g.links = make([][][]int32, n)
	g.locks = make([]sync.Mutex, n)
	r := rand.New(rand.NewSource(seed))
	ml := 1 / math.Log(float64(m))
	for i := range g.levels {
		l := int(-math.Log(1-r.Float64()) * ml)
		g.levels[i] = int8(l)
		g.links[i] = make([][]int32, l+1)
	}
	g.visited.New = func() any { return &visitSet{seen: make([]uint32, n)} }
	if n == 0 {
		return g
	}
	// порядок вставки — от высоких уровней к низким (как в Faiss): сначала
	// строится редкий верх графа, нижний слой цепляется к готовой навигации
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return g.levels[order[a]] > g.levels[order[b]] })
	g.insert(order[0])
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	next := make(chan int, 1024)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				g.insert(i)
			}
		}()
	}
	for _, i := range order[1:] {
		next <- i
	}
	close(next)
	wg.Wait()
	return g
}

func (g *Graph) maxLinks(level int) int {
	if level == 0 {
		return 2 * g.M
	}
	return g.M
}

// visitSet — отметки посещения с поколением: очистка за O(1) между поисками.
type visitSet struct {
	seen []uint32
	gen  uint32
}

func (v *visitSet) reset() {
	v.gen++
	if v.gen == 0 {
		clear(v.seen)
		v.gen = 1
	}
}

func (g *Graph) neighbors(id int32, level int, buf []int32) []int32 {
	g.locks[id].Lock()
	buf = append(buf[:0], g.links[id][level]...)
	g.locks[id].Unlock()
	return buf
}

// searchLayer — обход одного слоя от точек входа; возвращает до ef ближайших,
// от ближнего к дальнему. Счётчик dists — число вычисленных расстояний.
func (g *Graph) searchLayer(q []float32, eps []item, ef, level int, dists *int) []item {
	vs := g.visited.Get().(*visitSet)
	defer g.visited.Put(vs)
	vs.reset()
	var cand minHeap
	var best maxHeap
	for _, e := range eps {
		vs.seen[e.id] = vs.gen
		cand.push(e)
		best.push(e)
	}
	for len(best) > ef {
		best.pop()
	}
	var buf []int32
	for len(cand) > 0 {
		c := cand.pop()
		if c.d > best[0].d && len(best) >= ef {
			break
		}
		buf = g.neighbors(c.id, level, buf)
		for _, n := range buf {
			if vs.seen[n] == vs.gen {
				continue
			}
			vs.seen[n] = vs.gen
			d := g.Dist(q, g.vecs[n])
			*dists++
			if len(best) < ef || d < best[0].d {
				cand.push(item{n, d})
				best.push(item{n, d})
				if len(best) > ef {
					best.pop()
				}
			}
		}
	}
	out := make([]item, len(best))
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = best.pop()
	}
	return out
}

// selectNeighbors — эвристика из статьи (алгоритм 4): кандидат берётся, только
// если он ближе к базовому узлу, чем к любому уже выбранному соседу. Так граф
// сохраняет связи между кластерами, а не только внутри ближайшего.
func (g *Graph) selectNeighbors(cands []item, m int) []int32 {
	sort.Slice(cands, func(i, j int) bool { return cands[i].d < cands[j].d })
	out := make([]int32, 0, m)
	for _, c := range cands {
		if len(out) == m {
			break
		}
		ok := true
		for _, r := range out {
			if g.Dist(g.vecs[c.id], g.vecs[r]) < c.d {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, c.id)
		}
	}
	return out
}

func (g *Graph) insert(i int) {
	q := g.vecs[i]
	level := int(g.levels[i])
	g.mu.RLock()
	ep, top := g.entry, g.maxLevel
	g.mu.RUnlock()
	if ep < 0 {
		g.mu.Lock()
		if g.entry < 0 {
			g.entry, g.maxLevel = int32(i), level
			g.mu.Unlock()
			return
		}
		ep, top = g.entry, g.maxLevel
		g.mu.Unlock()
	}
	var dists int
	cur := item{ep, g.Dist(q, g.vecs[ep])}
	for lc := top; lc > level; lc-- {
		cur = g.greedy(q, cur, lc, &dists)
	}
	eps := []item{cur}
	for lc := min(top, level); lc >= 0; lc-- {
		w := g.searchLayer(q, eps, g.EfConstruction, lc, &dists)
		// соседей новому узлу — до предела слоя (на слое 0 — 2·M), как в Faiss и
		// pgvector; hnswlib берёт M и на слое 0
		nb := g.selectNeighbors(append([]item(nil), w...), g.maxLinks(lc))
		g.locks[i].Lock()
		g.links[i][lc] = nb
		g.locks[i].Unlock()
		for _, n := range nb {
			g.connect(n, int32(i), lc)
		}
		eps = w
	}
	if level > top {
		g.mu.Lock()
		if level > g.maxLevel {
			g.entry, g.maxLevel = int32(i), level
		}
		g.mu.Unlock()
	}
}

// connect добавляет обратную связь n → i; переполненный список ужимается той же эвристикой.
func (g *Graph) connect(n, i int32, level int) {
	g.locks[n].Lock()
	defer g.locks[n].Unlock()
	l := append(g.links[n][level], i)
	if len(l) <= g.maxLinks(level) {
		g.links[n][level] = l
		return
	}
	cands := make([]item, len(l))
	for j, x := range l {
		cands[j] = item{x, g.Dist(g.vecs[n], g.vecs[x])}
	}
	g.links[n][level] = g.selectNeighbors(cands, g.maxLinks(level))
}

func (g *Graph) greedy(q []float32, cur item, level int, dists *int) item {
	var buf []int32
	for changed := true; changed; {
		changed = false
		buf = g.neighbors(cur.id, level, buf)
		for _, n := range buf {
			d := g.Dist(q, g.vecs[n])
			*dists++
			if d < cur.d {
				cur, changed = item{n, d}, true
			}
		}
	}
	return cur
}

// Search — k ближайших при ширине обхода ef (ef ≥ k); второе значение —
// сколько расстояний посчитано.
func (g *Graph) Search(q []float32, k, ef int) ([]int32, int) {
	var dists int
	g.mu.RLock()
	ep, top := g.entry, g.maxLevel
	g.mu.RUnlock()
	cur := item{ep, g.Dist(q, g.vecs[ep])}
	for lc := top; lc > 0; lc-- {
		cur = g.greedy(q, cur, lc, &dists)
	}
	w := g.searchLayer(q, []item{cur}, max(ef, k), 0, &dists)
	out := make([]int32, 0, k)
	for _, x := range w[:min(k, len(w))] {
		out = append(out, x.id)
	}
	return out, dists
}

// LinkBytes — память списков соседей (по 4 байта на связь), без векторов.
func (g *Graph) LinkBytes() int64 {
	var s int64
	for _, ls := range g.links {
		for _, l := range ls {
			s += int64(len(l)) * 4
		}
	}
	return s
}

// Levels — сколько узлов на каждом слое.
func (g *Graph) Levels() []int {
	var out []int
	for _, l := range g.levels {
		for len(out) <= int(l) {
			out = append(out, 0)
		}
		for j := 0; j <= int(l); j++ {
			out[j]++
		}
	}
	return out
}

// Reachable — какие узлы достижимы по рёбрам слоя 0 из точки входа. Эвристика
// выбора соседей и обрезка переполненных списков могут оставить узел без
// входящих рёбер: такой узел поиск не найдёт ни при каком ef.
func (g *Graph) Reachable() []bool {
	seen := make([]bool, len(g.vecs))
	if g.entry < 0 {
		return seen
	}
	stack := []int32{g.entry}
	seen[g.entry] = true
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, x := range g.links[n][0] {
			if !seen[x] {
				seen[x] = true
				stack = append(stack, x)
			}
		}
	}
	return seen
}
