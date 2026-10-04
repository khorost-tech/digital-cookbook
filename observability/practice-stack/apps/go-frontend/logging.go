package main

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

// fanoutHandler пишет каждую запись двумя путями: JSON в stdout и OTLP через
// мост otelslog. Это не избыточность, а два разных маршрута доставки логов,
// которые в проде обычно сосуществуют: stdout подхватывает конвейер
// (Vector/Fluent Bit), OTLP уходит в Collector напрямую. В статье 3 видно, что
// корреляция с трейсом работает в обоих случаях, но по-разному достаётся.
type fanoutHandler struct {
	handlers []slog.Handler
}

func (f fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f fanoutHandler) Handle(ctx context.Context, rec slog.Record) error {
	for _, h := range f.handlers {
		if !h.Enabled(ctx, rec.Level) {
			continue
		}
		// Каждому хендлеру — своя копия записи: Record не рассчитан на
		// повторное потребление разными обработчиками.
		if err := h.Handle(ctx, rec.Clone()); err != nil {
			return err
		}
	}
	return nil
}

func (f fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, 0, len(f.handlers))
	for _, h := range f.handlers {
		next = append(next, h.WithAttrs(attrs))
	}
	return fanoutHandler{handlers: next}
}

func (f fanoutHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, 0, len(f.handlers))
	for _, h := range f.handlers {
		next = append(next, h.WithGroup(name))
	}
	return fanoutHandler{handlers: next}
}

// traceContextHandler добавляет trace_id и span_id в КАЖДУЮ запись stdout.
//
// Зачем это нужно отдельно от моста otelslog: мост кладёт контекст в OTLP-поток,
// а JSON в stdout остаётся без trace_id. То есть структурный лог получается, а
// коррелируемый — нет, и конвейер, читающий файл (Vector, Fluent Bit), связать
// запись с трейсом не сможет. В Java этого делать не приходится: агент кладёт
// trace_id в MDC, и логгер выводит его сам.
type traceContextHandler struct {
	slog.Handler
}

func (h traceContextHandler) Handle(ctx context.Context, rec slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		rec.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, rec)
}

func (h traceContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceContextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h traceContextHandler) WithGroup(name string) slog.Handler {
	return traceContextHandler{Handler: h.Handler.WithGroup(name)}
}

// newLogger собирает логгер. Если SDK не инициализирован, остаётся только
// stdout — и это ровно то поведение, которое надо показать: приложение
// логирует, но в бэкенде логов нет.
func newLogger(lp *sdklog.LoggerProvider) *slog.Logger {
	var stdout slog.Handler = traceContextHandler{
		Handler: slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}),
	}
	if lp == nil {
		return slog.New(stdout)
	}
	bridge := otelslog.NewHandler("go-frontend", otelslog.WithLoggerProvider(lp))
	return slog.New(fanoutHandler{handlers: []slog.Handler{stdout, bridge}})
}
