// Package values показывает правильную работу с context.WithValue:
// неэкспортируемый тип ключа (нет коллизий между пакетами), геттер с
// проверкой наличия и понимание, что WithValue — для request-scoped данных
// (requestID, trace, авторизация), а не для передачи параметров функций.
package values

import "context"

// ctxKey — неэкспортируемый тип ключа. Так значение из этого пакета нельзя
// перезаписать из чужого пакета (у него будет свой тип с тем же именем).
type ctxKey int

const (
	requestIDKey ctxKey = iota
	userIDKey
)

// WithRequestID кладёт request-scoped идентификатор запроса в контекст.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID достаёт идентификатор запроса. Второй результат — признак
// наличия: значение может отсутствовать, и это надо проверять, а не паниковать
// на type assertion.
func RequestID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey).(string)
	return id, ok
}

// WithUserID кладёт идентификатор пользователя.
func WithUserID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

// UserID достаёт идентификатор пользователя.
func UserID(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(userIDKey).(int64)
	return id, ok
}
