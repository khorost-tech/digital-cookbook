// Пакет quant — сжатие векторов: скалярная квантизация в байт на координату
// (SQ8) и product quantization (PQ): вектор режется на m подвекторов, каждый
// заменяется номером ближайшего из 256 центроидов своего подпространства.
// Поиск идёт по сжатым кодам, а R лучших кандидатов можно пересчитать по
// исходным векторам (rescoring).
package quant

import (
	"math"
	"sort"

	"vdb/ann/ivf"
	"vdb/ann/vec"
)

type cand struct {
	i int32
	s float32 // приближённый скор: больше — ближе
}

// topR — R лучших по приближённому скору.
func topR(n int, score func(i int) float32, r int) []cand {
	h := make([]cand, 0, r+1)
	for i := 0; i < n; i++ {
		s := score(i)
		if len(h) == r && s <= h[r-1].s {
			continue
		}
		j := len(h)
		for j > 0 && h[j-1].s < s {
			j--
		}
		h = append(h, cand{})
		copy(h[j+1:], h[j:])
		h[j] = cand{int32(i), s}
		if len(h) > r {
			h = h[:r]
		}
	}
	return h
}

// rescore — k лучших из кандидатов по точному скалярному произведению; r ≤ k —
// без пересчёта, по приближённому порядку.
func rescore(q []float32, cs []cand, k int, data [][]float32, exact bool) []int32 {
	if exact {
		for j := range cs {
			cs[j].s = vec.Dot(q, data[cs[j].i])
		}
		sort.Slice(cs, func(a, b int) bool {
			if cs[a].s != cs[b].s {
				return cs[a].s > cs[b].s
			}
			return cs[a].i < cs[b].i // как в истине: при равном скоре — меньший номер
		})
	}
	out := make([]int32, 0, k)
	for _, c := range cs[:min(k, len(cs))] {
		out = append(out, c.i)
	}
	return out
}

// SQ8 — по каждой координате свой диапазон [min, max] на 256 уровней.
type SQ8 struct {
	Min, Scale []float32
	Codes      []uint8 // N·d
	d          int
}

func TrainSQ8(data [][]float32) *SQ8 {
	d := len(data[0])
	q := &SQ8{Min: make([]float32, d), Scale: make([]float32, d), d: d}
	mx := make([]float32, d)
	for j := 0; j < d; j++ {
		q.Min[j], mx[j] = math.MaxFloat32, -math.MaxFloat32
	}
	for _, v := range data {
		for j, x := range v {
			q.Min[j] = min(q.Min[j], x)
			mx[j] = max(mx[j], x)
		}
	}
	for j := range q.Scale {
		q.Scale[j] = (mx[j] - q.Min[j]) / 255
	}
	q.Codes = make([]uint8, len(data)*d)
	for i, v := range data {
		for j, x := range v {
			c := math.Round(float64((x - q.Min[j]) / q.Scale[j]))
			q.Codes[i*d+j] = uint8(max(0, min(255, c)))
		}
	}
	return q
}

// Decode — восстановленный вектор (для оценки ошибки).
func (q *SQ8) Decode(i int) []float32 {
	out := make([]float32, q.d)
	for j := range out {
		out[j] = q.Min[j] + float32(q.Codes[i*q.d+j])*q.Scale[j]
	}
	return out
}

// Search — скор по кодам: q·x ≈ Σ q_j·min_j + Σ (q_j·scale_j)·c_j; затем r
// кандидатов, при rescore — пересчёт по исходным.
func (q *SQ8) Search(query []float32, k, r int, data [][]float32, exact bool) []int32 {
	var base float32
	w := make([]float32, q.d)
	for j := range w {
		base += query[j] * q.Min[j]
		w[j] = query[j] * q.Scale[j]
	}
	n := len(q.Codes) / q.d
	cs := topR(n, func(i int) float32 {
		c := q.Codes[i*q.d : (i+1)*q.d]
		var s0, s1, s2, s3 float32
		for j := 0; j+3 < len(c); j += 4 {
			s0 += w[j] * float32(c[j])
			s1 += w[j+1] * float32(c[j+1])
			s2 += w[j+2] * float32(c[j+2])
			s3 += w[j+3] * float32(c[j+3])
		}
		return base + s0 + s1 + s2 + s3
	}, max(r, k))
	return rescore(query, cs, k, data, exact)
}

// PQ — m подпространств по d/m координат, в каждом 256 центроидов.
type PQ struct {
	M, Sub    int
	Codebooks [][][]float32 // m × 256 × sub
	Codes     []uint8       // N·m
}

func TrainPQ(train, data [][]float32, m, iters int, seed int64) *PQ {
	d := len(data[0])
	p := &PQ{M: m, Sub: d / m}
	for s := 0; s < m; s++ {
		part := make([][]float32, len(train))
		for i, v := range train {
			part[i] = v[s*p.Sub : (s+1)*p.Sub]
		}
		p.Codebooks = append(p.Codebooks, ivf.KMeans(part, 256, iters, seed+int64(s)))
	}
	p.Codes = make([]uint8, len(data)*m)
	for s := 0; s < m; s++ {
		part := make([][]float32, len(data))
		for i, v := range data {
			part[i] = v[s*p.Sub : (s+1)*p.Sub]
		}
		assign := make([]int, len(data))
		ivf.Assign(part, p.Codebooks[s], assign)
		for i, a := range assign {
			p.Codes[i*m+s] = uint8(a)
		}
	}
	return p
}

// CodebookBytes — память кодбуков (float32).
func (p *PQ) CodebookBytes() int64 { return int64(p.M) * 256 * int64(p.Sub) * 4 }

// Search — асимметричный скор (ADC): таблица m×256 скалярных произведений
// подвектора запроса с центроидами, скор кода — сумма m чисел из таблицы.
func (p *PQ) Search(query []float32, k, r int, data [][]float32, exact bool) []int32 {
	table := make([]float32, p.M*256)
	for s := 0; s < p.M; s++ {
		qs := query[s*p.Sub : (s+1)*p.Sub]
		for c, cv := range p.Codebooks[s] {
			table[s*256+c] = vec.Dot(qs, cv)
		}
	}
	n := len(p.Codes) / p.M
	cs := topR(n, func(i int) float32 {
		var s float32
		for j, c := range p.Codes[i*p.M : (i+1)*p.M] {
			s += table[j*256+int(c)]
		}
		return s
	}, max(r, k))
	return rescore(query, cs, k, data, exact)
}
