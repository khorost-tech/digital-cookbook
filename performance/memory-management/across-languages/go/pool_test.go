// Опора 2b: давление на аллокатор, allocs/op с sync.Pool и без.
//
// Каждая аллокация в куче — работа для сборщика позже. sync.Pool переиспользует
// буферы между операциями, убирая аллокацию с горячего пути. Смотрим на allocs/op:
// это число ДЕТЕРМИНИРОВАНО (сколько раз за операцию runtime реально шёл в heap),
// в отличие от ns/op, который зависит от машины и не абсолютизируется.
//
// Прогон: go test -bench=. -benchmem -run='^$' ./...
package memmgmt

import (
	"sync"
	"testing"
)

const bufSize = 4096

// escSink — глобальный приёмник. Присваивание в него ЗАСТАВЛЯЕТ буфер "убежать"
// в кучу (иначе escape analysis оставил бы его на стеке и аллокации бы не было —
// именно это и надо показать контрастом с пулом).
var escSink []byte

// BenchmarkNoPool: свежий буфер на КАЖДУЮ операцию -> 1 alloc/op.
func BenchmarkNoPool(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf := make([]byte, bufSize) // аллокация в куче на каждой итерации
		buf[0] = byte(i)
		buf[bufSize-1] = byte(i)
		escSink = buf // уводим в heap
	}
}

var pool = sync.Pool{
	New: func() any {
		b := make([]byte, bufSize)
		return &b // храним *[]byte: указатель, чтобы Get/Put не аллоцировали в интерфейс
	},
}

// BenchmarkPool: буфер берётся из пула и возвращается -> амортизированно 0 allocs/op.
func BenchmarkPool(b *testing.B) {
	b.ReportAllocs()
	var sink byte
	for i := 0; i < b.N; i++ {
		bp := pool.Get().(*[]byte)
		buf := *bp
		buf[0] = byte(i)
		buf[bufSize-1] = byte(i)
		sink += buf[0] + buf[bufSize-1]
		pool.Put(bp)
	}
	_ = sink
}
