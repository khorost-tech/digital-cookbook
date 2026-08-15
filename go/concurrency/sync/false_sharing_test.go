// false_sharing_test.go — демонстрация ложного разделения (false sharing).
// Когда два счётчика лежат в одной кэш-линии (обычно 64 байта) и их
// обновляют разные ядра, каждая запись инвалидирует линию у соседа —
// ядра «пинают» линию туда-сюда. Паддинг разносит счётчики по разным
// линиям и убирает этот эффект.
//
// Числа не приводим намеренно: снимайте у себя
//   go test -bench=BenchmarkFalseSharing -benchmem ./sync/...
// и сравнивайте ns/op. На многоядерных машинах padded обычно заметно
// быстрее adjacent; на одноядерном прогоне разница может исчезнуть.
package synccookbook

import (
	"sync"
	"testing"
)

// adjacentCounters — два счётчика вплотную: рискуют попасть в одну линию.
type adjacentCounters struct {
	a int64
	b int64
}

// paddedCounters — те же счётчики, разведённые паддингом на разные линии.
type paddedCounters struct {
	a int64
	_ [64]byte // паддинг: следующий счётчик — уже в другой кэш-линии
	b int64
}

const falseSharingIters = 1_000_000

// BenchmarkFalseSharingAdjacent — две горутины бьют по соседним полям.
func BenchmarkFalseSharingAdjacent(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var c adjacentCounters
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < falseSharingIters; j++ {
				c.a++
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < falseSharingIters; j++ {
				c.b++
			}
		}()
		wg.Wait()
	}
}

// BenchmarkFalseSharingPadded — то же, но поля разнесены паддингом.
func BenchmarkFalseSharingPadded(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var c paddedCounters
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < falseSharingIters; j++ {
				c.a++
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < falseSharingIters; j++ {
				c.b++
			}
		}()
		wg.Wait()
	}
}
