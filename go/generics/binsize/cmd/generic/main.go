// Бинарь, инстанцирующий дженерик-функции для МНОГИХ разных типов.
//
// Каждая инстанциация SumTwoGeneric[T] по value-типу порождает копию кода по
// своей форме (GC-shape stenciling). Результаты печатаются, чтобы компилятор
// не выкинул вызовы как мёртвый код. Размер этого бинаря сравнивается с
// cmd/iface (см. README).
package main

import (
	"fmt"

	"tech.khorost/generics-cookbook/binsize"
)

func main() {
	// Явные инстанциации по разным формам value-типов.
	fmt.Println(binsize.SumTwoGeneric[int](1, 2))
	fmt.Println(binsize.SumTwoGeneric[int8](1, 2))
	fmt.Println(binsize.SumTwoGeneric[int16](1, 2))
	fmt.Println(binsize.SumTwoGeneric[int32](1, 2))
	fmt.Println(binsize.SumTwoGeneric[int64](1, 2))
	fmt.Println(binsize.SumTwoGeneric[uint](1, 2))
	fmt.Println(binsize.SumTwoGeneric[uint8](1, 2))
	fmt.Println(binsize.SumTwoGeneric[uint16](1, 2))
	fmt.Println(binsize.SumTwoGeneric[uint32](1, 2))
	fmt.Println(binsize.SumTwoGeneric[uint64](1, 2))
	fmt.Println(binsize.SumTwoGeneric[float32](1, 2))
	fmt.Println(binsize.SumTwoGeneric[float64](1, 2))
	fmt.Println(binsize.SumTwoGeneric[string]("a", "b"))

	// Zero[T] по тем же типам — ещё набор инстанциаций.
	fmt.Println(binsize.Zero[int]())
	fmt.Println(binsize.Zero[int8]())
	fmt.Println(binsize.Zero[int16]())
	fmt.Println(binsize.Zero[int32]())
	fmt.Println(binsize.Zero[int64]())
	fmt.Println(binsize.Zero[uint]())
	fmt.Println(binsize.Zero[uint8]())
	fmt.Println(binsize.Zero[uint16]())
	fmt.Println(binsize.Zero[uint32]())
	fmt.Println(binsize.Zero[uint64]())
	fmt.Println(binsize.Zero[float32]())
	fmt.Println(binsize.Zero[float64]())
	fmt.Println(binsize.Zero[string]())
}
