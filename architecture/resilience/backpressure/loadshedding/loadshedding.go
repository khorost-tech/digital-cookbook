// loadshedding.go: неблокирующий приём запросов со сбросом нагрузки под
// перегрузом. Ядро — Shedder: приём через select с веткой default (если места
// в очереди нет — запрос немедленно отклоняется, а не ставится в очередь).
// Плюс сигнал насыщения: по длине очереди (Saturated) и по сглаженной латентности
// обработки (тип EWMA). Так система защищает себя, отклоняя избыток вместо того,
// чтобы копить неограниченный backlog.
package loadshedding

import (
	"sync"
	"sync/atomic"
)

// EWMA — экспоненциально взвешенное скользящее среднее. Используется как сигнал
// насыщения по латентности: обновляется наблюдёнными замерами, значение плавно
// следует за трендом. Не для конкурентного доступа без внешней синхронизации —
// в Shedder обновляется под собственным мьютексом.
type EWMA struct {
	alpha float64 // вес нового замера, 0..1 (больше — быстрее реакция)
	value float64
	init  bool
}

// NewEWMA создаёт сглаживатель с коэффициентом alpha (0 < alpha <= 1).
func NewEWMA(alpha float64) *EWMA {
	if alpha <= 0 || alpha > 1 {
		panic("loadshedding: alpha must be in (0, 1]")
	}
	return &EWMA{alpha: alpha}
}

// Update добавляет замер sample и возвращает новое сглаженное значение. Первый
// замер задаёт начальное значение без сглаживания.
func (e *EWMA) Update(sample float64) float64 {
	if !e.init {
		e.value = sample
		e.init = true
		return e.value
	}
	e.value = e.alpha*sample + (1-e.alpha)*e.value
	return e.value
}

// Value возвращает текущее сглаженное значение (0, пока не было замеров).
func (e *EWMA) Value() float64 { return e.value }

// Shedder — приёмник запросов с ограниченной очередью и сбросом нагрузки.
// Запрос — это функция do, которую выполняет один из воркеров. Приём Submit
// неблокирующий: если очередь заполнена, запрос отклоняется. Безопасен для
// конкурентного вызова Submit из нескольких горутин.
type Shedder struct {
	queue     chan func()
	workers   int
	highWater int // порог длины очереди для сигнала насыщения

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	started  bool

	accepted  atomic.Int64
	rejected  atomic.Int64
	processed atomic.Int64

	mu   sync.Mutex
	ewma *EWMA // сглаженная латентность обработки (для наблюдаемости)
}

// Config задаёт параметры Shedder.
type Config struct {
	Capacity  int     // ёмкость очереди приёма
	Workers   int     // число обработчиков
	HighWater int     // длина очереди, с которой считаем систему насыщенной (0 => Capacity)
	Alpha     float64 // коэффициент EWMA для латентности (0 => 0.2)
}

// New создаёт Shedder по конфигурации. Не запускает воркеров — вызовите Start.
func New(cfg Config) *Shedder {
	if cfg.Capacity < 1 {
		panic("loadshedding: Capacity must be >= 1")
	}
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	if cfg.HighWater <= 0 || cfg.HighWater > cfg.Capacity {
		cfg.HighWater = cfg.Capacity
	}
	if cfg.Alpha <= 0 {
		cfg.Alpha = 0.2
	}
	return &Shedder{
		queue:     make(chan func(), cfg.Capacity),
		workers:   cfg.Workers,
		highWater: cfg.HighWater,
		stop:      make(chan struct{}),
		ewma:      NewEWMA(cfg.Alpha),
	}
}

// Submit пытается принять запрос do. Возвращает false, если очередь заполнена и
// запрос отклонён (сброс нагрузки). Не блокирует вызывающего.
func (s *Shedder) Submit(do func()) bool {
	select {
	case s.queue <- do:
		s.accepted.Add(1)
		return true
	default:
		s.rejected.Add(1)
		return false
	}
}

// Start запускает воркеров. Повторный вызов игнорируется.
func (s *Shedder) Start() {
	if s.started {
		return
	}
	s.started = true
	for i := 0; i < s.workers; i++ {
		s.wg.Add(1)
		go s.worker()
	}
}

func (s *Shedder) worker() {
	defer s.wg.Done()
	for {
		select {
		case do := <-s.queue:
			s.run(do)
		case <-s.stop:
			// Дренаж: доигрываем уже принятые запросы, чтобы «принятое
			// обрабатывается» соблюдалось и при остановке.
			for {
				select {
				case do := <-s.queue:
					s.run(do)
				default:
					return
				}
			}
		}
	}
}

func (s *Shedder) run(do func()) {
	do()
	s.processed.Add(1)
}

// Observe вносит замер латентности latencyMillis в сглаживатель. Демо вызывает
// его после обработки запроса (замер снимается в рантайме).
func (s *Shedder) Observe(latencyMillis float64) {
	s.mu.Lock()
	s.ewma.Update(latencyMillis)
	s.mu.Unlock()
}

// AvgLatency возвращает текущую сглаженную латентность обработки (мс).
func (s *Shedder) AvgLatency() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ewma.Value()
}

// Saturated сообщает, что система насыщена: длина очереди достигла порога
// highWater. Может использоваться приёмником, чтобы заранее отклонять запросы.
func (s *Shedder) Saturated() bool {
	return len(s.queue) >= s.highWater
}

// Len возвращает текущую длину очереди приёма.
func (s *Shedder) Len() int { return len(s.queue) }

// Stop останавливает воркеров, дренируя уже принятые запросы, и ждёт завершения.
// После Stop вызывать Submit не следует. Идемпотентен.
func (s *Shedder) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.wg.Wait()
}

// Accepted возвращает число принятых запросов за всё время.
func (s *Shedder) Accepted() int64 { return s.accepted.Load() }

// Rejected возвращает число отклонённых (сброшенных) запросов за всё время.
func (s *Shedder) Rejected() int64 { return s.rejected.Load() }

// Processed возвращает число фактически обработанных запросов за всё время.
func (s *Shedder) Processed() int64 { return s.processed.Load() }
