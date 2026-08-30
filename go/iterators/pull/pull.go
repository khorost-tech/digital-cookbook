// Package pull показывает iter.Pull — превращение «пуш-итератора» iter.Seq
// в «пул-итератор» с функцией next(), которую вызывают вручную. Это нужно,
// когда обход двух последовательностей идёт «в ногу» (zip, слияние) — то,
// что не выразить одним range.
package pull

import "iter"

// Zip идёт по двум последовательностям параллельно и выдаёт пары, пока
// не кончится любая из них. Реализуемо только через Pull: один range не
// умеет продвигать два источника синхронно.
func Zip[A, B any](a iter.Seq[A], b iter.Seq[B]) iter.Seq2[A, B] {
	return func(yield func(A, B) bool) {
		nextA, stopA := iter.Pull(a)
		defer stopA() // обязательно освободить ресурсы пул-итератора
		nextB, stopB := iter.Pull(b)
		defer stopB()

		for {
			av, okA := nextA()
			bv, okB := nextB()
			if !okA || !okB { // хотя бы одна кончилась
				return
			}
			if !yield(av, bv) {
				return
			}
		}
	}
}

// Take возвращает первые n элементов через ручное продвижение next().
func Take[V any](src iter.Seq[V], n int) []V {
	next, stop := iter.Pull(src)
	defer stop()

	out := make([]V, 0, n)
	for i := 0; i < n; i++ {
		v, ok := next()
		if !ok {
			break
		}
		out = append(out, v)
	}
	return out
}
