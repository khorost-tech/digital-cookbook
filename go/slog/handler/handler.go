// Package handler показывает, как написать собственный slog.Handler.
// ContextHandler оборачивает любой другой хендлер и на каждом Handle
// достаёт request_id из context.Context, добавляя его в запись, — так
// идентификатор запроса не нужно передавать в каждый вызов лога вручную.
package handler

import (
	"context"
	"log/slog"
)

// ctxKey — приватный тип ключа контекста (без коллизий между пакетами).
type ctxKey int

const requestIDKey ctxKey = 0

// WithRequestID кладёт request_id в контекст. Дальше его подхватит ContextHandler.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// ContextHandler — декоратор над slog.Handler. Реализует все четыре метода
// интерфейса: Enabled, Handle, WithAttrs, WithGroup — делегируя обёрнутому
// хендлеру, но обогащая Record в Handle.
type ContextHandler struct {
	slog.Handler
}

// New оборачивает переданный хендлер.
func New(h slog.Handler) *ContextHandler {
	return &ContextHandler{Handler: h}
}

// Handle — единственный метод с собственной логикой: если в контексте есть
// request_id, добавляем его в запись перед передачей вниз.
func (h *ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs и WithGroup обязаны вернуть хендлер того же типа, иначе
// декоратор «потеряется» при накоплении атрибутов через logger.With.
func (h *ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ContextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *ContextHandler) WithGroup(name string) slog.Handler {
	return &ContextHandler{Handler: h.Handler.WithGroup(name)}
}
