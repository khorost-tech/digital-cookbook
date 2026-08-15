// ratelimit.go демонстрирует ограничение частоты обработки двумя способами.
//
// 1. golang.org/x/time/rate.Limiter — токен-бакет: Wait блокируется до выдачи
//    разрешения (уважает ctx), Allow даёт неблокирующую проверку «есть ли токен
//    прямо сейчас». Параметры — частота пополнения r (событий/сек) и размер
//    бакета burst.
// 2. TickerLimiter — простой лимитер на time.Ticker: разрешение приходит на
//    каждый тик. Владелец обязан вызвать Stop, иначе тикер продолжит работать
//    (утечка ресурса).
package patterns

import (
	"context"
	"time"

	"golang.org/x/time/rate"
)

// WaitProcess обрабатывает inputs, пропуская каждый элемент через блокирующий
// Wait токен-бакета: fn вызывается не чаще, чем позволяет лимитер lim. Wait
// уважает ctx — при отмене возвращает ошибку и прекращает обработку.
func WaitProcess[T, R any](ctx context.Context, lim *rate.Limiter, inputs []T, fn func(T) R) ([]R, error) {
	out := make([]R, 0, len(inputs))
	for _, in := range inputs {
		if err := lim.Wait(ctx); err != nil {
			return out, err // отмена ctx или дедлайн — останавливаемся.
		}
		out = append(out, fn(in))
	}
	return out, nil
}

// AllowFilter применяет fn только к тем элементам, для которых лимитер lim
// выдаёт разрешение прямо сейчас (Allow == true). Остальные элементы пропускаются
// без блокировки — типично для сброса лишней нагрузки (load shedding).
func AllowFilter[T, R any](lim *rate.Limiter, inputs []T, fn func(T) R) []R {
	out := make([]R, 0, len(inputs))
	for _, in := range inputs {
		if lim.Allow() {
			out = append(out, fn(in))
		}
	}
	return out
}

// TickerLimiter — простой лимитер частоты на time.Ticker. Разрешение доступно
// через канал C на каждом тике. Stop обязателен для освобождения тикера.
type TickerLimiter struct {
	ticker *time.Ticker
	// C — канал разрешений; читатель дожидается тика перед следующим действием.
	C <-chan time.Time
}

// NewTickerLimiter создаёт лимитер с интервалом interval между разрешениями.
func NewTickerLimiter(interval time.Duration) *TickerLimiter {
	t := time.NewTicker(interval)
	return &TickerLimiter{ticker: t, C: t.C}
}

// Stop останавливает внутренний тикер. Обязателен во избежание утечки ресурса.
func (l *TickerLimiter) Stop() { l.ticker.Stop() }
