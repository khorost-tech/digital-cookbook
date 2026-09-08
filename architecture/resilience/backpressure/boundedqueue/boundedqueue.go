// boundedqueue.go: ограниченная по ёмкости очередь BoundedQueue[T] с четырьмя
// политиками поведения при переполнении. Это ядро самозащиты под перегрузом:
// backpressure (Block), сброс нагрузки (DropNewest/DropOldest) и быстрый отказ
// (Reject).
//
// Внутри — буферизованный канал ёмкости cap. Канал даёт синхронизацию «из
// коробки»: доступ к элементам между отправителями и получателями упорядочен
// happens-before, отдельного мьютекса для данных не нужно. Счётчики accepted /
// dropped ведутся через atomic.Int64, поэтому тип безопасен для конкурентного
// использования несколькими producer'ами и consumer'ами.
package boundedqueue

import (
	"context"
	"sync/atomic"
)

// Policy — политика поведения Offer при полной очереди.
type Policy int

const (
	// Block — backpressure: отправитель ждёт освобождения места либо отмены ctx.
	Block Policy = iota
	// DropNewest — сброс нагрузки: новый элемент отбрасывается, очередь не меняется.
	DropNewest
	// DropOldest — сброс нагрузки: вытесняется самый старый элемент, новый входит.
	DropOldest
	// Reject — быстрый отказ (аналог HTTP 503): элемент не принимается немедленно.
	Reject
)

// String возвращает читаемое имя политики (для логов и вывода демо).
func (p Policy) String() string {
	switch p {
	case Block:
		return "Block"
	case DropNewest:
		return "DropNewest"
	case DropOldest:
		return "DropOldest"
	case Reject:
		return "Reject"
	default:
		return "Unknown"
	}
}

// BoundedQueue[T] — потокобезопасная очередь фиксированной ёмкости с политикой
// переполнения. Создаётся через New. Пригодна для нескольких отправителей и
// получателей одновременно.
type BoundedQueue[T any] struct {
	ch       chan T
	policy   Policy
	accepted atomic.Int64 // сколько элементов вошло в очередь через Offer
	dropped  atomic.Int64 // сколько элементов отброшено (новый или вытесненный старый)
}

// New создаёт очередь ёмкости capacity с политикой policy. capacity должен быть
// не меньше 1 — иначе функция паникует, потому что политики DropOldest/DropNewest
// на нулевой ёмкости вырождаются.
func New[T any](capacity int, policy Policy) *BoundedQueue[T] {
	if capacity < 1 {
		panic("boundedqueue: capacity must be >= 1")
	}
	return &BoundedQueue[T]{
		ch:     make(chan T, capacity),
		policy: policy,
	}
}

// Offer пытается поместить item в очередь согласно политике.
//
// Возвращает accepted == true, если элемент вошёл в очередь (для DropOldest это
// значит «новый принят», а вытесненный старый при этом учитывается как dropped).
// accepted == false означает, что элемент item не попал в очередь: для Block —
// из-за отмены ctx, для DropNewest/Reject — из-за переполнения.
func (q *BoundedQueue[T]) Offer(ctx context.Context, item T) (accepted bool) {
	switch q.policy {
	case Block:
		// Backpressure: ждём место либо отмену контекста.
		select {
		case q.ch <- item:
			q.accepted.Add(1)
			return true
		case <-ctx.Done():
			q.dropped.Add(1)
			return false
		}

	case DropNewest, Reject:
		// Неблокирующая попытка: если места нет — сразу отбрасываем/отклоняем.
		// Семантика DropNewest и Reject для одиночного Offer совпадает
		// (новый элемент не принят), различие проявляется в трактовке на
		// стороне вызова: Reject — это явный отказ клиенту (503).
		select {
		case q.ch <- item:
			q.accepted.Add(1)
			return true
		default:
			q.dropped.Add(1)
			return false
		}

	case DropOldest:
		// Освобождаем место, вытесняя самый старый элемент, и вставляем новый.
		// Цикл нужен на случай гонки: между неудачной отправкой и вытеснением
		// получатель мог сам забрать элемент — тогда повторяем попытку отправки.
		for {
			select {
			case q.ch <- item:
				q.accepted.Add(1)
				return true
			default:
				select {
				case <-q.ch:
					q.dropped.Add(1) // вытеснили старый элемент
				default:
					// Очередь опустела конкурентно — повторяем отправку.
				}
			}
		}

	default:
		panic("boundedqueue: unknown policy")
	}
}

// Take извлекает элемент из очереди, блокируясь до его появления либо до отмены
// ctx. ok == false означает, что ctx отменён и элемент не получен.
func (q *BoundedQueue[T]) Take(ctx context.Context) (item T, ok bool) {
	select {
	case v := <-q.ch:
		return v, true
	case <-ctx.Done():
		var zero T
		return zero, false
	}
}

// TryTake — неблокирующее извлечение: ok == false, если очередь пуста.
func (q *BoundedQueue[T]) TryTake() (item T, ok bool) {
	select {
	case v := <-q.ch:
		return v, true
	default:
		var zero T
		return zero, false
	}
}

// Len возвращает текущее число элементов в очереди (мгновенный снимок).
func (q *BoundedQueue[T]) Len() int { return len(q.ch) }

// Cap возвращает ёмкость очереди.
func (q *BoundedQueue[T]) Cap() int { return cap(q.ch) }

// Accepted возвращает число элементов, вошедших в очередь за всё время.
func (q *BoundedQueue[T]) Accepted() int64 { return q.accepted.Load() }

// Dropped возвращает число отброшенных элементов за всё время (для DropOldest
// сюда попадают вытесненные старые элементы).
func (q *BoundedQueue[T]) Dropped() int64 { return q.dropped.Load() }
