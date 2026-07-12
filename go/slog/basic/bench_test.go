package basic

import (
	"context"
	"io"
	"testing"
	"log/slog"
)

// Бенчи сравнивают два способа записи одной и той же строки лога.
// Вывод направлен в io.Discard, чтобы мерить стоимость сборки записи, а не I/O.

// BenchmarkInfoKV — привычный вызов с вариативными парами key/value.
// В этом бенче аргументы константны и не «убегают», поэтому escape-анализ
// держит вариативный []any на стеке — 0 аллокаций, наравне с LogAttrs.
func BenchmarkInfoKV(b *testing.B) {
	l := slog.New(slog.NewJSONHandler(io.Discard, nil))
	b.ReportAllocs()
	for b.Loop() {
		l.Info("http request", "path", "/users/42", "status", 200)
	}
}

// BenchmarkLogAttrs — тот же результат через LogAttrs с готовыми slog.Attr:
// вариативный []any не строится в принципе. Выигрыш не в «здесь быстрее»
// (числа близки), а в предсказуемости: путь не зависит от того, сумеет ли
// компилятор оставить []any на стеке.
func BenchmarkLogAttrs(b *testing.B) {
	l := slog.New(slog.NewJSONHandler(io.Discard, nil))
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		l.LogAttrs(ctx, slog.LevelInfo, "http request",
			slog.String("path", "/users/42"),
			slog.Int("status", 200),
		)
	}
}

// BenchmarkDisabledLevel — запись ниже порога уровня. slog отбрасывает её
// рано (Handler.Enabled), почти без работы: важно, что «выключенные» логи
// в горячем пути дёшевы.
func BenchmarkDisabledLevel(b *testing.B) {
	l := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		l.LogAttrs(ctx, slog.LevelInfo, "http request",
			slog.String("path", "/users/42"),
			slog.Int("status", 200),
		)
	}
}
