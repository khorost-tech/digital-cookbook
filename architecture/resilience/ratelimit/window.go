package ratelimit

import "time"

// FixedWindow — счётчик в фиксированном окне, выровненном по часам (каждую
// секунду/минуту счётчик обнуляется). Прост и дёшев, но у него есть известный
// изъян на СТЫКЕ окон: limit запросов в конце одного окна и ещё limit в начале
// следующего дают 2×limit за интервал короче окна. Этот изъян стенд и меряет.
type FixedWindow struct {
	limit  int
	window time.Duration
	start  time.Time
	count  int
}

func NewFixedWindow(limit int, window time.Duration) *FixedWindow {
	return &FixedWindow{limit: limit, window: window}
}

func (w *FixedWindow) AllowAt(now time.Time) Decision {
	winStart := now.Truncate(w.window) // выравнивание по границам окна
	if !winStart.Equal(w.start) {
		w.start = winStart
		w.count = 0
	}
	if w.count < w.limit {
		w.count++
		return Decision{Allowed: true}
	}
	return Decision{RetryAfter: w.start.Add(w.window).Sub(now)}
}

// SlidingWindowLog — точное скользящее окно: хранит метки времени пропущенных
// запросов и на каждом запросе выкидывает те, что старше окна. В ЛЮБОМ интервале
// длиной window проходит не больше limit — стыка окон нет. Платит памятью:
// до limit меток на ключ.
type SlidingWindowLog struct {
	limit  int
	window time.Duration
	times  []time.Time
}

func NewSlidingWindowLog(limit int, window time.Duration) *SlidingWindowLog {
	return &SlidingWindowLog{limit: limit, window: window}
}

func (w *SlidingWindowLog) AllowAt(now time.Time) Decision {
	cutoff := now.Add(-w.window)
	i := 0
	for i < len(w.times) && !w.times[i].After(cutoff) { // окно (now-window, now]
		i++
	}
	w.times = w.times[i:]
	if len(w.times) < w.limit {
		w.times = append(w.times, now)
		return Decision{Allowed: true}
	}
	return Decision{RetryAfter: w.times[0].Add(w.window).Sub(now)}
}

// SlidingWindowCounter — приближение скользящего окна двумя счётчиками (текущее
// и предыдущее выровненные окна) с линейным весом по тому, насколько мы вошли в
// текущее окно. Память O(1) на ключ, точность — приблизительная: гасит стык
// фиксированного окна, но на краю может ошибаться на доли limit.
type SlidingWindowCounter struct {
	limit    int
	window   time.Duration
	curStart time.Time
	cur      int
	prev     int
}

func NewSlidingWindowCounter(limit int, window time.Duration) *SlidingWindowCounter {
	return &SlidingWindowCounter{limit: limit, window: window}
}

func (w *SlidingWindowCounter) AllowAt(now time.Time) Decision {
	winStart := now.Truncate(w.window)
	switch {
	case w.curStart.IsZero():
		w.curStart = winStart
	case winStart.Equal(w.curStart.Add(w.window)):
		w.prev, w.cur, w.curStart = w.cur, 0, winStart
	case !winStart.Equal(w.curStart):
		w.prev, w.cur, w.curStart = 0, 0, winStart
	}
	elapsed := now.Sub(w.curStart).Seconds() / w.window.Seconds() // доля текущего окна [0,1)
	estimate := float64(w.prev)*(1-elapsed) + float64(w.cur)
	if estimate < float64(w.limit) {
		w.cur++
		return Decision{Allowed: true}
	}
	return Decision{RetryAfter: w.curStart.Add(w.window).Sub(now)}
}
