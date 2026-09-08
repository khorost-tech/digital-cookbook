// Package breaker — circuit breaker с тремя состояниями. Предохранитель между
// сервисом и зависимостью: пока зависимость здорова — closed (пропускаем всё);
// после серии ошибок — open (мгновенно отказываем, не долбим мёртвую зависимость
// и не копим заблокированные вызовы); спустя паузу — half-open (пробуем несколько
// запросов и решаем, вернулась ли зависимость).
//
// Время передаётся явно (AllowAt/Record принимают now) — переходы состояний
// тестируются детерминированно, без сна.
package breaker

import (
	"sync"
	"time"
)

// State — состояние предохранителя.
type State int

const (
	Closed   State = iota // пропускаем вызовы, считаем ошибки
	Open                  // отказываем сразу, ждём паузу
	HalfOpen              // пропускаем пробные вызовы
)

func (s State) String() string {
	switch s {
	case Closed:
		return "closed"
	case Open:
		return "open"
	case HalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Breaker безопасен для конкурентного использования.
type Breaker struct {
	failThreshold int           // подряд ошибок для размыкания
	openFor       time.Duration // пауза в open перед пробой
	halfOpenMax   int           // сколько пробных вызовов пропустить в half-open

	mu       sync.Mutex
	state    State
	fails    int       // подряд ошибок в closed
	openedAt time.Time // когда перешли в open
	halfOpen int       // сколько проб уже пропущено
	halfOK   int       // сколько проб удалось
}

// New: failThreshold — сколько ошибок подряд размыкают цепь; openFor — пауза
// перед пробой; halfOpenMax — сколько пробных вызовов пропустить.
func New(failThreshold int, openFor time.Duration, halfOpenMax int) *Breaker {
	if failThreshold < 1 || halfOpenMax < 1 {
		panic("breaker: failThreshold и halfOpenMax должны быть >= 1")
	}
	return &Breaker{failThreshold: failThreshold, openFor: openFor, halfOpenMax: halfOpenMax}
}

// AllowAt сообщает, пропустить ли вызов в момент now, обновляя состояние по
// таймауту (open → half-open). Half-open пропускает не больше halfOpenMax проб.
func (b *Breaker) AllowAt(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case Open:
		if now.Sub(b.openedAt) >= b.openFor {
			b.state = HalfOpen
			b.halfOpen = 1
			b.halfOK = 0
			return true
		}
		return false
	case HalfOpen:
		if b.halfOpen < b.halfOpenMax {
			b.halfOpen++
			return true
		}
		return false
	default: // Closed
		return true
	}
}

// Record учитывает исход пропущенного вызова.
func (b *Breaker) Record(now time.Time, success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case Closed:
		if success {
			b.fails = 0
		} else if b.fails++; b.fails >= b.failThreshold {
			b.trip(now)
		}
	case HalfOpen:
		if !success {
			b.trip(now) // проба провалилась — снова размыкаем
			return
		}
		if b.halfOK++; b.halfOK >= b.halfOpenMax {
			b.reset() // все пробы удались — зависимость вернулась
		}
	case Open:
		// исход в open не ожидается (вызовы не пропускаются), игнорируем
	}
}

func (b *Breaker) trip(now time.Time) {
	b.state = Open
	b.openedAt = now
	b.fails = 0
}

func (b *Breaker) reset() {
	b.state = Closed
	b.fails = 0
	b.halfOpen = 0
	b.halfOK = 0
}

// State возвращает текущее состояние (для наблюдаемости/тестов).
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}
