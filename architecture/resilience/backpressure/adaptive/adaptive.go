// adaptive.go: адаптивный лимитер параллелизма по схеме AIMD (Additive Increase
// / Multiplicative Decrease), в духе Netflix concurrency-limits. Лимит числа
// одновременно выполняемых операций не фиксирован: при росте наблюдаемой
// латентности выше порога лимит уменьшается мультипликативно (быстрый откат от
// перегрузки зависимости), при нормальной латентности — растёт аддитивно
// (осторожное прощупывание пропускной способности).
//
// Механика ожидания под лимитом повторяет golang.org/x/sync/semaphore: очередь
// ждущих, каждому — свой канал ready; Release/рост лимита будят ждущих по
// порядку. Отдельных фоновых горутин нет, отмена ctx корректно снимает waiter'а
// из очереди — значит, утечек горутин не возникает.
package adaptive

import (
	"container/list"
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Config задаёт параметры AIMD-лимитера.
type Config struct {
	Min       int           // нижняя граница лимита (>= 1)
	Max       int           // верхняя граница лимита
	Initial   int           // стартовый лимит (в пределах [Min, Max])
	Threshold time.Duration // порог латентности: выше — снижаем лимит
	Increase  float64       // аддитивный шаг роста при норме (>0), обычно 1
	Decrease  float64       // мультипликативный множитель снижения (0<d<1), напр. 0.9
}

func (c *Config) normalize() {
	if c.Min < 1 {
		c.Min = 1
	}
	if c.Max < c.Min {
		c.Max = c.Min
	}
	if c.Initial < c.Min {
		c.Initial = c.Min
	}
	if c.Initial > c.Max {
		c.Initial = c.Max
	}
	if c.Increase <= 0 {
		c.Increase = 1
	}
	if c.Decrease <= 0 || c.Decrease >= 1 {
		c.Decrease = 0.9
	}
	if c.Threshold <= 0 {
		c.Threshold = 50 * time.Millisecond
	}
}

// Limiter — адаптивный лимитер параллелизма. Acquire занимает слот (ожидая, если
// лимит исчерпан), Release освобождает слот и по замеру латентности обновляет
// лимит по AIMD. Безопасен для конкурентного использования.
type Limiter struct {
	cfg Config

	mu       sync.Mutex
	limit    float64   // текущий лимит (дробный, для плавного AIMD)
	inFlight int       // сколько слотов занято сейчас
	waiters  list.List // очередь ждущих *waiter (FIFO)

	updates atomic.Int64 // сколько раз лимит пересчитывался (для наблюдаемости)
}

type waiter struct {
	ready chan struct{}
}

// New создаёт лимитер по конфигурации.
func New(cfg Config) *Limiter {
	cfg.normalize()
	l := &Limiter{cfg: cfg, limit: float64(cfg.Initial)}
	l.waiters.Init()
	return l
}

// Acquire занимает слот параллелизма. Если лимит исчерпан, блокирует до
// освобождения слота (или роста лимита) либо до отмены ctx. Возвращает ошибку
// ctx при отмене; в этом случае слот не занят.
func (l *Limiter) Acquire(ctx context.Context) error {
	// Уважаем уже отменённый ctx даже на быстром пути: иначе вызывающий, который
	// в цикле берёт и отпускает слот, никогда не заметит отмену.
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	// Быстрый путь: есть свободный слот и никто не ждёт впереди.
	if l.waiters.Len() == 0 && l.inFlight < int(l.limit) {
		l.inFlight++
		l.mu.Unlock()
		return nil
	}
	w := &waiter{ready: make(chan struct{})}
	elem := l.waiters.PushBack(w)
	l.mu.Unlock()

	select {
	case <-ctx.Done():
		l.mu.Lock()
		select {
		case <-w.ready:
			// Нас успели наделить слотом между отменой и захватом мьютекса —
			// корректно возвращаем слот обратно.
			l.releaseSlotLocked()
		default:
			l.waiters.Remove(elem)
		}
		l.mu.Unlock()
		return ctx.Err()
	case <-w.ready:
		// Слот уже занят за нас (inFlight увеличен при пробуждении).
		return nil
	}
}

// Release освобождает слот и обновляет лимит по замеру латентности sample
// последней операции (AIMD). Затем будит ждущих, если появилось место.
func (l *Limiter) Release(sample time.Duration) {
	l.mu.Lock()
	l.inFlight--
	l.updateLimitLocked(sample)
	l.wakeLocked()
	l.mu.Unlock()
}

// releaseSlotLocked возвращает слот без обновления лимита (для пути отмены ctx,
// когда работа не выполнялась и замера латентности нет).
func (l *Limiter) releaseSlotLocked() {
	l.inFlight--
	l.wakeLocked()
}

// updateLimitLocked применяет правило AIMD по замеру sample.
func (l *Limiter) updateLimitLocked(sample time.Duration) {
	if sample > l.cfg.Threshold {
		// Перегруз зависимости: мультипликативное снижение.
		l.limit *= l.cfg.Decrease
	} else {
		// Норма: аддитивный рост.
		l.limit += l.cfg.Increase
	}
	if l.limit < float64(l.cfg.Min) {
		l.limit = float64(l.cfg.Min)
	}
	if l.limit > float64(l.cfg.Max) {
		l.limit = float64(l.cfg.Max)
	}
	l.updates.Add(1)
}

// wakeLocked будит ждущих в порядке очереди, пока есть свободные слоты. При
// пробуждении слот сразу засчитывается за waiter'а (inFlight++), поэтому в
// Acquire повторный инкремент не нужен.
func (l *Limiter) wakeLocked() {
	for l.waiters.Len() > 0 && l.inFlight < int(l.limit) {
		e := l.waiters.Front()
		l.waiters.Remove(e)
		w := e.Value.(*waiter)
		l.inFlight++
		close(w.ready)
	}
}

// Limit возвращает текущий целочисленный лимит параллелизма.
func (l *Limiter) Limit() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return int(l.limit)
}

// InFlight возвращает число занятых слотов в момент вызова.
func (l *Limiter) InFlight() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inFlight
}

// Updates возвращает число пересчётов лимита за всё время (наблюдаемость).
func (l *Limiter) Updates() int64 { return l.updates.Load() }
