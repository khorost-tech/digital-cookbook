// Package bulkhead — изоляция ресурсов по «отсекам» (как переборки в корпусе
// корабля: пробоина в одном отсеке не топит весь корпус). Каждый отсек — своя
// ёмкость слотов. Жадный потребитель, заполнивший свой отсек, не отбирает места
// у других; в общем пуле он бы съел всё и заблокировал остальных.
package bulkhead

import (
	"errors"
	"sync"
)

// ErrFull — в отсеке нет свободных слотов.
var ErrFull = errors.New("bulkhead: отсек заполнен")

// ErrUnknown — запрошен несуществующий отсек.
var ErrUnknown = errors.New("bulkhead: неизвестный отсек")

// Bulkhead — набор именованных отсеков с независимой ёмкостью каждый.
// Слоты реализованы буферизованными каналами (семафор). Безопасен конкурентно.
type Bulkhead struct {
	slots map[string]chan struct{}
}

// New создаёт отсеки по карте «имя → ёмкость».
func New(capacities map[string]int) *Bulkhead {
	slots := make(map[string]chan struct{}, len(capacities))
	for name, c := range capacities {
		if c < 1 {
			panic("bulkhead: ёмкость отсека должна быть >= 1")
		}
		slots[name] = make(chan struct{}, c)
	}
	return &Bulkhead{slots: slots}
}

// TryAcquire занимает слот в отсеке key без блокировки. Возвращает release для
// освобождения слота либо ErrFull, если отсек полон (ErrUnknown — нет отсека).
func (b *Bulkhead) TryAcquire(key string) (release func(), err error) {
	ch, ok := b.slots[key]
	if !ok {
		return nil, ErrUnknown
	}
	select {
	case ch <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-ch }) }, nil
	default:
		return nil, ErrFull
	}
}

// InUse возвращает число занятых слотов в отсеке (для наблюдаемости/тестов).
func (b *Bulkhead) InUse(key string) int {
	if ch, ok := b.slots[key]; ok {
		return len(ch)
	}
	return 0
}
