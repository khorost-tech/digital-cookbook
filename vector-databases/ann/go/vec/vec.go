// Пакет vec — векторы датасета серии, расстояния и recall.
package vec

import (
	"bufio"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Dim — размерность e5-small.
const Dim = 384

// Dot — скалярное произведение; развёрнуто на 4, чтобы компилятор держал
// четыре независимых суммы.
func Dot(a, b []float32) float32 {
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

// L2 — квадрат евклидова расстояния.
func L2(a, b []float32) float32 {
	var s0, s1, s2, s3 float32
	n := len(a) &^ 3
	for i := 0; i < n; i += 4 {
		d0, d1, d2, d3 := a[i]-b[i], a[i+1]-b[i+1], a[i+2]-b[i+2], a[i+3]-b[i+3]
		s0 += d0 * d0
		s1 += d1 * d1
		s2 += d2 * d2
		s3 += d3 * d3
	}
	for i := n; i < len(a); i++ {
		d := a[i] - b[i]
		s0 += d * d
	}
	return s0 + s1 + s2 + s3
}

// Расстояния: меньше — ближе. Для нормированных векторов CosineDist = 1 − Dot.
func CosineDist(a, b []float32) float32 { return 1 - Dot(a, b) }
func DotDist(a, b []float32) float32    { return -Dot(a, b) }

// Cosine — честный косинус, без предположения о нормировке.
func Cosine(a, b []float32) float32 {
	return Dot(a, b) / float32(math.Sqrt(float64(Dot(a, a))*float64(Dot(b, b))))
}

func CosineFullDist(a, b []float32) float32 { return 1 - Cosine(a, b) }

// ReadF32 читает файл float32 little-endian по Dim на строку; limit > 0 — первые limit строк.
func ReadF32(path string, limit int) ([][]float32, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	n := len(b) / 4 / Dim
	if limit > 0 && limit < n {
		n = limit
	}
	flat := make([]float32, n*Dim)
	for i := range flat {
		flat[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	out := make([][]float32, n)
	for i := range out {
		out[i] = flat[i*Dim : (i+1)*Dim : (i+1)*Dim]
	}
	return out, nil
}

// GT — истина точного перебора из стенда dataset.
type GT struct {
	Rows []int     // номера запросов в queries.f32
	IDs  [][]int32 // по K ближайших строк корпуса
}

func ReadGT(dir string, k int) (*GT, error) {
	f, err := os.Open(filepath.Join(dir, "gt.rows"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	g := &GT{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		r, err := strconv.Atoi(strings.TrimSpace(sc.Text()))
		if err != nil {
			return nil, err
		}
		g.Rows = append(g.Rows, r)
	}
	b, err := os.ReadFile(filepath.Join(dir, "gt.i32"))
	if err != nil {
		return nil, err
	}
	for i := range g.Rows {
		ids := make([]int32, k)
		for j := range ids {
			ids[j] = int32(binary.LittleEndian.Uint32(b[4*(i*k+j):]))
		}
		g.IDs = append(g.IDs, ids)
	}
	return g, nil
}

// Recall — доля истинных k ближайших в выдаче (выдача длиной k).
func Recall(got []int32, truth []int32, k int) float64 {
	want := make(map[int32]bool, k)
	for _, t := range truth[:k] {
		want[t] = true
	}
	hit := 0
	for i, g := range got {
		if i == k {
			break
		}
		if want[g] {
			hit++
		}
	}
	return float64(hit) / float64(k)
}

// Exact — точный перебор: k ближайших по dist, при равенстве — меньший номер.
func Exact(q []float32, data [][]float32, k int, dist func(a, b []float32) float32) []int32 {
	type hit struct {
		i int32
		d float32
	}
	h := make([]hit, 0, k+1)
	for i, v := range data {
		d := dist(q, v)
		if len(h) == k && d >= h[k-1].d {
			continue
		}
		j := len(h)
		for j > 0 && h[j-1].d > d {
			j--
		}
		h = append(h, hit{})
		copy(h[j+1:], h[j:])
		h[j] = hit{int32(i), d}
		if len(h) > k {
			h = h[:k]
		}
	}
	out := make([]int32, len(h))
	for i, x := range h {
		out[i] = x.i
	}
	return out
}
