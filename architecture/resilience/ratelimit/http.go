package ratelimit

import (
	"math"
	"net/http"
	"strconv"
	"time"
)

// Middleware оборачивает handler лимитером. Отклонённый запрос получает честный
// отказ: статус 429 Too Many Requests и заголовок Retry-After в секундах — а не
// тихую потерю и не зависший коннект.
func Middleware(next http.Handler, lim Limiter) http.Handler {
	return middlewareWithClock(next, lim, time.Now)
}

// middlewareWithClock — та же обёртка с инъекцией времени: в проде now = time.Now,
// в тестах — фиксированные часы, чтобы решение не зависело от того, пересёк ли
// запрос настенную границу секунды (иначе тест был бы flaky).
func middlewareWithClock(next http.Handler, lim Limiter, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		d := lim.AllowAt(now())
		if d.Allowed {
			next.ServeHTTP(rw, r)
			return
		}
		secs := int(math.Ceil(d.RetryAfter.Seconds()))
		if secs < 1 {
			secs = 1
		}
		rw.Header().Set("Retry-After", strconv.Itoa(secs))
		http.Error(rw, "rate limit exceeded", http.StatusTooManyRequests)
	})
}
