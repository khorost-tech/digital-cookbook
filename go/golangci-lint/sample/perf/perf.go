// Package perf — лишние аллокации и подозрительные идиомы.
package perf

import (
	"fmt"
	"strconv"
)

// IDs растит срез в цикле, хотя длина известна заранее. Контракт: на пустом
// входе — nil (в JSON — null).
func IDs(n []int) []string {
	var out []string // want prealloc // kind opt
	for _, v := range n {
		out = append(out, strconv.Itoa(v))
	}
	return out
}

// Key форматирует число через Sprintf.
func Key(n int) string {
	return fmt.Sprintf("%d", n) // want perfsprint // kind opt
}

// Merge склеивает a и b. Контракт: результат — новый срез, не делящий память
// с a; append в другой срез нарушает его, когда у a есть запас ёмкости.
func Merge(a, b []int) []int {
	c := append(a, b...) // want gocritic
	return c
}
