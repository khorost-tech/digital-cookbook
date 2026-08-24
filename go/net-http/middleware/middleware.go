// middleware.go — middleware в идиоме net/http: func(http.Handler) http.Handler.
// Каждый слой оборачивает следующий обработчик, добавляя поведение до/после
// вызова. Здесь: логирование (счётчик), recover (паника → контролируемый 500),
// проброс request-id через context. Chain склеивает несколько слоёв в цепочку.
package middleware

import (
	"context"
	"net/http"
	"sync/atomic"
)

// Middleware — канонический тип: принимает http.Handler, возвращает http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain оборачивает h слоями mws. Применяем в обратном порядке, чтобы при
// вызове Chain(h, mw1, mw2) внешним оказался mw1: запрос идёт mw1 → mw2 → h.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// Logger считает число прошедших запросов через переданный счётчик. В реальном
// сервисе тут был бы slog; для стенда счётчик делает эффект наблюдаемым в тесте.
func Logger(counter *atomic.Int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			next.ServeHTTP(w, r)
		})
	}
}

// Recover перехватывает панику в любом нижележащем обработчике через defer +
// recover и превращает её в контролируемый 500. Важно: сам net/http.Server и
// без этого слоя перехватывает панику обработчика (логирует стектрейс и
// обрывает соединение), так что паника одного запроса не роняет процесс. Слой
// нужен ради АККУРАТНОГО ответа клиенту (500 в своём формате вместо
// оборванного соединения) и единообразных логов/метрик, а не ради спасения
// процесса. Оговорка: чистый 500 выйдет, только если ответ ещё не начали
// писать; если заголовки уже ушли, http.Error не сможет переиграть статус
// (тест покрывает панику ДО записи).
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				http.Error(w, "внутренняя ошибка", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// contextKey — приватный тип ключа контекста, чтобы исключить коллизии с
// ключами из других пакетов (string-ключи в context — известный источник
// пересечений).
type contextKey string

const requestIDKey contextKey = "request-id"

// RequestID кладёт идентификатор в контекст запроса. Значение id задаётся
// фабрикой (в проде — генератор UUID); стенд принимает его явно ради
// детерминированного теста.
func RequestID(id string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), requestIDKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestIDFromContext достаёт request-id из контекста. Второй результат —
// признак наличия, как у чтения из map.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey).(string)
	return id, ok
}

// MaxBytes ограничивает размер тела запроса n байтами через http.MaxBytesReader.
// Барьер против DoS «по объёму»: без него обработчик, читающий тело целиком
// (io.ReadAll, JSON-декодер), примет сколько угодно и выест память. При
// превышении лимита чтение тела возвращает ошибку "http: request body too large",
// а само соединение помечается как не пригодное для переиспользования; ответить
// клиенту (обычно 413) должен обработчик ниже по цепочке.
func MaxBytes(n int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}
