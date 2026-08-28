// dispatch_test.go — бенчмарк стоимости диспетчеризации.
//
// Три случая, и разница между ними — сам урок:
//   Direct — прямой вызов метода конкретного типа (компилятор инлайнит);
//   Mono   — вызов через интерфейс, но конкретный тип на call-site ОДИН и
//            известен статически: компилятор часто ДЕВИРТУАЛИЗИРУЕТ вызов,
//            и он выходит ≈ прямому;
//   Poly   — call-site видит НЕСКОЛЬКО конкретных типов (индекс переменный),
//            девиртуализировать нельзя, вызов идёт косвенно через itab —
//            здесь видна реальная цена интерфейсной диспетчеризации.
//
// Числа снимайте у себя (из папки interfaces/):
//   go test -bench=BenchmarkDispatch -benchmem -run='^$' .
// либо из корня go-fundamentals:
//   go test -bench=BenchmarkDispatch -benchmem -run='^$' ./interfaces/
package interfaces

import "testing"

// counter и counter2 — два разных конкретных типа с одним методом Add.
type counter struct{ n int }

func (c *counter) Add(x int) { c.n += x }

type counter2 struct{ n int }

func (c *counter2) Add(x int) { c.n += 2 * x }

// adder — интерфейс над методом Add.
type adder interface{ Add(x int) }

// sink не даёт компилятору выкинуть вычисления как «мёртвые».
var sink int

// BenchmarkDispatchDirect — прямой вызов метода конкретного *counter.
func BenchmarkDispatchDirect(b *testing.B) {
	b.ReportAllocs()
	c := &counter{}
	for i := 0; i < b.N; i++ {
		c.Add(1)
	}
	sink = c.n
}

// BenchmarkDispatchMono — вызов через интерфейс с единственным статически
// известным типом: компилятор обычно девиртуализирует до прямого.
func BenchmarkDispatchMono(b *testing.B) {
	b.ReportAllocs()
	c := &counter{}
	var a adder = c
	for i := 0; i < b.N; i++ {
		a.Add(1)
	}
	sink = c.n
}

// BenchmarkDispatchPoly — полиморфный call-site: два конкретных типа в срезе,
// индекс переменный. Девиртуализация невозможна — вызов идёт через itab.
func BenchmarkDispatchPoly(b *testing.B) {
	b.ReportAllocs()
	c0 := &counter{}
	c1 := &counter2{}
	as := []adder{c0, c1}
	for i := 0; i < b.N; i++ {
		as[i&1].Add(1) // тип не выводится статически: косвенный вызов
	}
	sink = c0.n + c1.n
}
