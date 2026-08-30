// Package seq показывает пользовательские итераторы Go 1.23:
// iter.Seq[V] и iter.Seq2[K,V] — это функции-«пуш-итераторы», получающие
// yield и вызывающие его на каждом элементе. Возврат false из yield (ранний
// break на стороне range) обязывает итератор прекратить выдачу.
package seq

import "iter"

// Count возвращает iter.Seq, выдающий 0..n-1. Простейший генератор:
// на каждом шаге зовём yield и уважаем его ответ.
func Count(n int) iter.Seq[int] {
	return func(yield func(int) bool) {
		for i := 0; i < n; i++ {
			if !yield(i) { // range сделал break — прекращаем
				return
			}
		}
	}
}

// Filter оборачивает исходную последовательность, пропуская только элементы,
// удовлетворяющие keep. Композиция итераторов — это обычные функции.
func Filter[V any](src iter.Seq[V], keep func(V) bool) iter.Seq[V] {
	return func(yield func(V) bool) {
		for v := range src {
			if !keep(v) {
				continue
			}
			if !yield(v) {
				return
			}
		}
	}
}

// Map трансформирует элементы одной последовательности в другую.
func Map[A, B any](src iter.Seq[A], f func(A) B) iter.Seq[B] {
	return func(yield func(B) bool) {
		for a := range src {
			if !yield(f(a)) {
				return
			}
		}
	}
}

// Enumerate превращает iter.Seq[V] в iter.Seq2[int, V] — индекс + значение,
// как встроенный range по слайсу.
func Enumerate[V any](src iter.Seq[V]) iter.Seq2[int, V] {
	return func(yield func(int, V) bool) {
		i := 0
		for v := range src {
			if !yield(i, v) {
				return
			}
			i++
		}
	}
}

// WithCleanup выдаёт элементы src и ГАРАНТИРОВАННО выполняет cleanup при
// завершении обхода — в том числе при раннем break, потому что defer
// сработает, когда функция-итератор вернёт управление. Так закрывают файлы,
// строки БД, соединения внутри итератора.
func WithCleanup[V any](src iter.Seq[V], cleanup func()) iter.Seq[V] {
	return func(yield func(V) bool) {
		defer cleanup()
		for v := range src {
			if !yield(v) {
				return // cleanup всё равно выполнится через defer
			}
		}
	}
}

// Collect материализует последовательность в слайс (аналог slices.Collect).
func Collect[V any](src iter.Seq[V]) []V {
	var out []V
	for v := range src {
		out = append(out, v)
	}
	return out
}
