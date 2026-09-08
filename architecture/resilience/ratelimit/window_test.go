package ratelimit

import (
	"testing"
	"time"
)

// base — метка, выровненная по границе секундного окна (Unix 1000.000).
var base = time.Unix(1000, 0)

// countAllowed прогоняет n запросов в один и тот же момент now.
func countAllowed(l Limiter, now time.Time, n int) int {
	allowed := 0
	for i := 0; i < n; i++ {
		if l.AllowAt(now).Allowed {
			allowed++
		}
	}
	return allowed
}

// TestBoundaryBurst — заглавный замер статьи: всплеск на СТЫКЕ окон.
// Лимит 100/сек. Шлём 100 запросов в конце одного окна (base+900ms) и ещё 100
// в начале следующего (base+1000ms) — всё это внутри интервала в 100 мс.
// Фиксированное окно пропустит 200 (2×лимит), скользящее — ровно 100.
func TestBoundaryBurst(t *testing.T) {
	const limit = 100
	const window = time.Second
	tEnd := base.Add(900 * time.Millisecond)   // конец окна [1000,1001)
	tNext := base.Add(1000 * time.Millisecond) // начало окна [1001,1002)

	cases := []struct {
		name string
		lim  Limiter
		want int // сколько пройдёт за стык при честном скользящем поведении
	}{
		{"fixed-window", NewFixedWindow(limit, window), 200},
		{"sliding-log", NewSlidingWindowLog(limit, window), 100},
		{"sliding-counter", NewSlidingWindowCounter(limit, window), 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := countAllowed(c.lim, tEnd, limit) + countAllowed(c.lim, tNext, limit)
			if got != c.want {
				t.Fatalf("на стыке окон пропущено %d, ожидали %d", got, c.want)
			}
		})
	}
}

// TestSlidingHoldsInAnyWindow — скользящее окно не пропускает больше limit ни в
// каком выровненном подокне: проверяем несколько сдвигов внутри секунды.
func TestSlidingHoldsInAnyWindow(t *testing.T) {
	const limit = 10
	l := NewSlidingWindowLog(limit, time.Second)
	// 10 в момент base — заполнили окно
	if got := countAllowed(l, base, limit); got != limit {
		t.Fatalf("первое окно: пропущено %d, ждали %d", got, limit)
	}
	// на протяжении почти всей секунды новые запросы отвергаются
	for _, off := range []time.Duration{100, 300, 500, 900} {
		if d := l.AllowAt(base.Add(off * time.Millisecond)); d.Allowed {
			t.Fatalf("на +%dms скользящее окно пропустило сверх лимита", off)
		}
	}
	// ровно через секунду после первого запроса место освобождается
	if d := l.AllowAt(base.Add(1001 * time.Millisecond)); !d.Allowed {
		t.Fatal("через секунду место должно освободиться")
	}
}

// TestRetryAfterPositive — у отклонённого запроса Retry-After строго положителен.
func TestRetryAfterPositive(t *testing.T) {
	l := NewFixedWindow(1, time.Second)
	if d := l.AllowAt(base); !d.Allowed {
		t.Fatal("первый должен пройти")
	}
	d := l.AllowAt(base.Add(200 * time.Millisecond))
	if d.Allowed || d.RetryAfter <= 0 {
		t.Fatalf("ждали отказ с положительным Retry-After, получили %+v", d)
	}
}
