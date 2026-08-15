// cas.go — цикл compare-and-swap для операций, которых нет среди готовых
// атомиков. Классика — атомарный максимум: читаем текущее значение,
// вычисляем кандидата и пытаемся записать; при неудаче (кто-то опередил)
// перечитываем и повторяем.
package synccookbook

import "sync/atomic"

// AtomicMax хранит наибольшее из предъявленных значений без блокировок.
type AtomicMax struct {
	value atomic.Int64
}

// Observe обновляет максимум до v, если v больше текущего.
// CompareAndSwap возвращает false, когда значение поменялось между
// Load и попыткой записи, — тогда крутим цикл заново.
func (m *AtomicMax) Observe(v int64) {
	for {
		cur := m.value.Load()
		if v <= cur {
			return // текущий максимум уже не меньше — делать нечего
		}
		if m.value.CompareAndSwap(cur, v) {
			return // успешно продвинули максимум
		}
		// Проиграли гонку — повторяем с актуальным cur.
	}
}

// Max возвращает накопленный максимум.
func (m *AtomicMax) Max() int64 {
	return m.value.Load()
}
