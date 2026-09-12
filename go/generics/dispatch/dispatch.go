// Package dispatch — контринтуитивный сюжет про дженерики Go.
//
// Здесь дженерик НЕ ускоряет вызов метода. Причина в реализации:
// при GC-shape stenciling все указательные типы (*Circle, *Rect, ...) имеют
// одну и ту же форму (машинное слово-указатель) и делят ОДНУ копию кода
// специализации. Чтобы внутри этой общей копии вызвать нужный Area(),
// компилятор передаёт скрытый параметр — словарь (dictionary) с указателями
// на методы конкретного типа, и вызов идёт через словарь.
//
// Итог: для указательных типов дженерик-вызов через constraint по стоимости
// сопоставим с обычным интерфейсным dispatch (косвенный вызов через таблицу),
// а иногда чуть медленнее из-за лишнего косвенного обращения к словарю.
// Девиртуализации (превращения в прямой вызов) здесь НЕ происходит.
// Числа снимаются бенчами в dispatch_test.go.
package dispatch

import "math"

// Shape — интерфейс с одним методом.
type Shape interface {
	Area() float64
}

// Circle — реализация с указательным ресивером.
type Circle struct {
	R float64
}

// Area — площадь круга.
func (c *Circle) Area() float64 {
	return math.Pi * c.R * c.R
}

// Rect — вторая реализация с указательным ресивером.
type Rect struct {
	W, H float64
}

// Area — площадь прямоугольника.
func (r *Rect) Area() float64 {
	return r.W * r.H
}

// SumAreasGeneric — сумма площадей через constraint Shape.
// Вызов x.Area() для указательного T идёт через словарь специализации.
func SumAreasGeneric[T Shape](xs []T) float64 {
	var acc float64
	for _, x := range xs {
		acc += x.Area()
	}
	return acc
}

// SumAreasIface — та же сумма через обычный интерфейсный dispatch.
func SumAreasIface(xs []Shape) float64 {
	var acc float64
	for _, x := range xs {
		acc += x.Area()
	}
	return acc
}
