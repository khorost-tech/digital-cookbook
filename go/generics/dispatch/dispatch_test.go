// Тесты корректности и бенчмарки для контринтуитивного сюжета.
//
// Дженерик можно инстанцировать только одним конкретным типом, поэтому бенч
// _Generic работает по []*Circle. Бенч _Iface работает по []Shape с теми же
// кругами — сравниваем именно стоимость вызова метода при одинаковом наборе
// объектов. Ожидаемая картина (числа снимайте у себя): дженерик НЕ быстрее
// интерфейса; для указательных типов вызов через словарь сопоставим с
// интерфейсным dispatch (иногда чуть медленнее). Это и есть ключевой сюрприз.
package dispatch

import (
	"math"
	"testing"
)

// makeCircles — детерминированный срез кругов.
func makeCircles(n int) []*Circle {
	xs := make([]*Circle, n)
	for i := range xs {
		xs[i] = &Circle{R: float64(i%10) + 1}
	}
	return xs
}

// asShapes — те же круги как []Shape (боксинга нет: указатель кладётся в
// интерфейс без аллокации).
func asShapes(cs []*Circle) []Shape {
	out := make([]Shape, len(cs))
	for i, c := range cs {
		out[i] = c
	}
	return out
}

func TestSumAreasConsistency(t *testing.T) {
	cs := makeCircles(500)
	shapes := asShapes(cs)

	g := SumAreasGeneric(cs)
	i := SumAreasIface(shapes)
	if math.Abs(g-i) > 1e-9 {
		t.Fatalf("SumAreasGeneric = %v, SumAreasIface = %v", g, i)
	}
}

func TestMixedShapes(t *testing.T) {
	// Интерфейсный вариант умеет смешивать типы; проверяем полиморфизм.
	shapes := []Shape{
		&Circle{R: 1},
		&Rect{W: 2, H: 3},
		&Circle{R: 2},
	}
	want := math.Pi*1*1 + 2*3 + math.Pi*2*2
	if got := SumAreasIface(shapes); math.Abs(got-want) > 1e-9 {
		t.Fatalf("SumAreasIface = %v, want %v", got, want)
	}
}

// sink — чтобы компилятор не выкинул результат бенча.
var sink float64

const benchN = 4096

func BenchmarkArea_Generic(b *testing.B) {
	cs := makeCircles(benchN)
	b.ReportAllocs()
	b.ResetTimer()
	var acc float64
	for i := 0; i < b.N; i++ {
		acc += SumAreasGeneric(cs)
	}
	sink = acc
}

func BenchmarkArea_Iface(b *testing.B) {
	shapes := asShapes(makeCircles(benchN))
	b.ReportAllocs()
	b.ResetTimer()
	var acc float64
	for i := 0; i < b.N; i++ {
		acc += SumAreasIface(shapes)
	}
	sink = acc
}
