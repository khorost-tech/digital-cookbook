// Package basic собирает базовые приёмы log/slog (Go 1.21): уровни,
// типизированные атрибуты, группы, постоянный контекст через With и
// аллокационно-дешёвый путь LogAttrs. Вывод пишем в переданный io.Writer,
// чтобы тесты могли его прочитать и проверить.
package basic

import (
	"context"
	"io"
	"log/slog"
)

// JSONLogger строит логгер с JSON-хендлером, пишущим в w. Уровень —
// минимальный отображаемый (Debug/Info/Warn/Error).
func JSONLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// TextLogger строит логгер с текстовым (key=value) хендлером.
func TextLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// LogRequest пишет одну структурированную запись про HTTP-запрос.
// Атрибуты — типизированные (slog.String/Int/Duration), не «сырые» пары.
func LogRequest(l *slog.Logger, method, path string, status int) {
	l.Info("http request",
		slog.String("method", method),
		slog.String("path", path),
		slog.Int("status", status),
	)
}

// WithRequestID возвращает дочерний логгер, у которого во ВСЕ последующие
// записи автоматически подставляется request_id. With — это «запомнить
// контекст один раз».
func WithRequestID(l *slog.Logger, id string) *slog.Logger {
	return l.With(slog.String("request_id", id))
}

// LogInGroup кладёт связанные поля в группу — в JSON это вложенный объект
// "db": {"query": ..., "rows": ...}.
func LogInGroup(l *slog.Logger, query string, rows int) {
	l.Info("db query",
		slog.Group("db",
			slog.String("query", query),
			slog.Int("rows", rows),
		),
	)
}

// LogFast — «горячий путь»: LogAttrs принимает context и уже готовые Attr
// без вариативного []any. Функциональный результат тот же, что у Info(...);
// разница — не «быстрее здесь», а предсказуемость (не полагаемся на то, что
// escape-анализ оставит []any на стеке). Числа — в README стенда.
func LogFast(ctx context.Context, l *slog.Logger, path string, status int) {
	l.LogAttrs(ctx, slog.LevelInfo, "http request",
		slog.String("path", path),
		slog.Int("status", status),
	)
}
