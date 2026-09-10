// receiver_bench_test.go — цена ресивера на БОЛЬШОЙ структуре.
//
// ⚠️ Первая редакция этого бенча измеряла не то, что заявляла. Метод с
// value-ресивером читал одно поле (`h.data[0]`), и компилятор, заинлайнив
// вызов, копию структуры просто не создавал: копировать 2 КБ ради одного
// элемента незачем. Заявленной разницы «value в ~100 раз дороже» на Go 1.27
// нет — оба варианта идут за доли наносекунды.
//
// Копия ресивера — не то, что написано в сигнатуре, а то, что компилятор
// решил не устранять. Поэтому здесь три ПАРЫ бенчмарков, каждая отвечает на
// свой вопрос:
//
//	Inlined  — как в обычном коде: вызов инлайнится, копия элиминируется.
//	           Показывает, сколько стоит value-ресивер на практике.
//	NoInline — //go:noinline запрещает инлайн, и копия становится
//	           обязательной по соглашению о вызовах: аргумент передаётся по
//	           значению. Показывает цену САМОГО копирования 2 КБ.
//	FullRead — метод читает весь массив. Показывает, что как только у метода
//	           появляется настоящая работа, разница ресиверов тонет в ней.
//
// Числа снимайте у себя — в стенде их намеренно нет:
//
//	go test -bench=BenchmarkReceiver -benchmem -run='^$' ./methods/
package methods

import "testing"

// heavy — большая структура: массив [256]int (2 КБ на amd64).
type heavy struct {
	data [256]int
}

// --- пара 1: как в обычном коде (инлайн разрешён) ---

func (h heavy) touchValue() int { return h.data[0] }
func (h *heavy) touchPtr() int  { return h.data[0] }

// --- пара 2: инлайн запрещён, копия обязательна по ABI ---

//go:noinline
func (h heavy) touchValueNoInline() int { return h.data[0] }

//go:noinline
func (h *heavy) touchPtrNoInline() int { return h.data[0] }

// --- пара 3: метод реально использует весь массив ---

//go:noinline
func (h heavy) sumValue() int {
	s := 0
	for _, v := range h.data {
		s += v
	}
	return s
}

//go:noinline
func (h *heavy) sumPtr() int {
	s := 0
	for _, v := range h.data {
		s += v
	}
	return s
}

// benchSink не даёт устранить результат вызовов оптимизатору.
var benchSink int

func benchLoop(b *testing.B, call func() int) {
	b.ReportAllocs()
	s := 0
	for i := 0; i < b.N; i++ {
		s += call()
	}
	benchSink = s
}

// Пара 1 — вызов инлайнится, копии [256]int не возникает вовсе.
func BenchmarkReceiverValueInlined(b *testing.B) {
	h := heavy{}
	benchLoop(b, func() int { return h.touchValue() })
}

func BenchmarkReceiverPointerInlined(b *testing.B) {
	h := &heavy{}
	benchLoop(b, func() int { return h.touchPtr() })
}

// Пара 2 — инлайн запрещён: value-вызов обязан скопировать 2 КБ на стек.
func BenchmarkReceiverValueNoInline(b *testing.B) {
	h := heavy{}
	benchLoop(b, func() int { return h.touchValueNoInline() })
}

func BenchmarkReceiverPointerNoInline(b *testing.B) {
	h := &heavy{}
	benchLoop(b, func() int { return h.touchPtrNoInline() })
}

// Пара 3 — у метода есть настоящая работа: чтение всех 256 элементов.
func BenchmarkReceiverValueFullRead(b *testing.B) {
	h := heavy{}
	benchLoop(b, func() int { return h.sumValue() })
}

func BenchmarkReceiverPointerFullRead(b *testing.B) {
	h := &heavy{}
	benchLoop(b, func() int { return h.sumPtr() })
}
