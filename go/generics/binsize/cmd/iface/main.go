// Бинарь, делающий то же, что cmd/generic, но через interface{}.
//
// Все типы проходят через ОДНУ функцию SumTwoIface(any, any) any — отдельных
// копий кода по формам не возникает. Результаты печатаются, чтобы вызовы не
// выкинул компилятор. Размер этого бинаря сравнивается с cmd/generic (README):
// у Go разница скромная, потому что shape stenciling шарит копии по форме и не
// делает полную мономорфизацию, как C++.
package main

import (
	"fmt"

	"tech.khorost/generics-cookbook/binsize"
)

func main() {
	fmt.Println(binsize.SumTwoIface(int(1), int(2)))
	fmt.Println(binsize.SumTwoIface(int8(1), int8(2)))
	fmt.Println(binsize.SumTwoIface(int16(1), int16(2)))
	fmt.Println(binsize.SumTwoIface(int32(1), int32(2)))
	fmt.Println(binsize.SumTwoIface(int64(1), int64(2)))
	fmt.Println(binsize.SumTwoIface(uint(1), uint(2)))
	fmt.Println(binsize.SumTwoIface(uint8(1), uint8(2)))
	fmt.Println(binsize.SumTwoIface(uint16(1), uint16(2)))
	fmt.Println(binsize.SumTwoIface(uint32(1), uint32(2)))
	fmt.Println(binsize.SumTwoIface(uint64(1), uint64(2)))
	fmt.Println(binsize.SumTwoIface(float32(1), float32(2)))
	fmt.Println(binsize.SumTwoIface(float64(1), float64(2)))
	fmt.Println(binsize.SumTwoIface("a", "b"))
}
