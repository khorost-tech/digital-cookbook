// receiver_bench_test.go — бенчмарк цены ресивера на БОЛЬШОЙ структуре.
//
// Идея: у метода с value-ресивером каждый вызов копирует всю структуру целиком
// (здесь — массив [256]int), у метода с pointer-ресивером копируется только
// указатель. На больших типах это заметная разница по времени и по аллокациям.
//
// Числа снимайте у себя — в стенде их намеренно нет:
//   go test -bench=BenchmarkReceiver -benchmem -run='^$' ./methods/
package methods

import "testing"

// heavy — большая структура: массив [256]int делает копирование ощутимым.
type heavy struct {
	data [256]int
}

// touchValue — VALUE-ресивер: при каждом вызове heavy копируется целиком.
// Возвращаем data[0], чтобы компилятор не выкинул вызов как «мёртвый».
func (h heavy) touchValue() int {
	return h.data[0]
}

// touchPtr — POINTER-ресивер: копируется лишь указатель, не массив.
func (h *heavy) touchPtr() int {
	return h.data[0]
}

// benchSink не даёт устранить результат вызовов оптимизатору.
var benchSink int

// BenchmarkReceiverValue — вызов метода на value-ресивере: копия всей структуры
// на каждой итерации.
func BenchmarkReceiverValue(b *testing.B) {
	b.ReportAllocs()
	h := heavy{}
	s := 0
	for i := 0; i < b.N; i++ {
		s += h.touchValue()
	}
	benchSink = s
}

// BenchmarkReceiverPointer — вызов метода на pointer-ресивере: копируется только
// указатель, массив не трогается.
func BenchmarkReceiverPointer(b *testing.B) {
	b.ReportAllocs()
	h := &heavy{}
	s := 0
	for i := 0; i < b.N; i++ {
		s += h.touchPtr()
	}
	benchSink = s
}
