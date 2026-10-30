// Package perf — исправленная пара sample/perf.
package perf

import "strconv"

// IDs выделяет срез нужной длины сразу и сохраняет контракт: на пустом входе — nil.
func IDs(n []int) []string {
	if len(n) == 0 {
		return nil
	}
	out := make([]string, 0, len(n))
	for _, v := range n {
		out = append(out, strconv.Itoa(v))
	}
	return out
}

// Key через strconv.
func Key(n int) string {
	return strconv.Itoa(n)
}

// Merge копирует, не трогая a.
func Merge(a, b []int) []int {
	c := make([]int, 0, len(a)+len(b))
	c = append(c, a...)
	return append(c, b...)
}
