// Демо: как выглядит реальный вывод slog в разных хендлерах.
//
// Печатает одни и те же записи через JSON- и text-хендлер, показывает
// группы, With и подстановку request_id из контекста кастомным хендлером.
//
// Запуск:
//
//	go run ./cmd/output
package main

import (
	"context"
	"fmt"
	"os"
	"log/slog"

	"tech.khorost/go-slog-cookbook/handler"
	"tech.khorost/go-slog-cookbook/redact"
)

func main() {
	fmt.Println("=== JSON-хендлер ===")
	j := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	j.Info("http request",
		slog.String("method", "GET"),
		slog.String("path", "/users/42"),
		slog.Int("status", 200),
	)
	j.Warn("slow query",
		slog.Group("db",
			slog.String("query", "SELECT ..."),
			slog.Duration("took", 0),
		),
	)

	fmt.Println("\n=== Text-хендлер (key=value) ===")
	t := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	t.Info("http request", slog.String("method", "GET"), slog.Int("status", 200))

	fmt.Println("\n=== With: постоянный атрибут во всех записях ===")
	svc := j.With(slog.String("service", "api"), slog.String("request_id", "req-7"))
	svc.Info("started")
	svc.Info("finished")

	fmt.Println("\n=== Кастомный ContextHandler: request_id из контекста ===")
	c := slog.New(handler.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx := handler.WithRequestID(context.Background(), "req-from-ctx")
	c.InfoContext(ctx, "обработка", slog.String("path", "/checkout"))

	fmt.Println("\n=== Маскировка: LogValuer (тип-секрет сам подставляет маску) ===")
	r := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	r.Info("login",
		slog.String("user", "alice"),
		slog.Any("token", redact.Secret("s3cr3t-abcdef")),
		slog.Any("card", redact.Card("4111111111111234")),
	)

	fmt.Println("\n=== Маскировка: ReplaceAttr (страховочная сетка по ключу) ===")
	rr := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: redact.RedactKeys("password", "authorization"),
	}))
	rr.Info("request",
		slog.String("user", "alice"),
		slog.String("password", "hunter2"),
		slog.String("authorization", "Bearer xyz"),
	)

	fmt.Println("\n=== Граница: Secret-поле внутри структуры НЕ маскируется (LogValue не зовётся) ===")
	// Token имеет тип redact.Secret — как отдельный атрибут дал бы "REDACTED",
	// но внутри структуры, сериализуемой целиком, его LogValue не вызывается.
	type creds struct {
		User  string
		Token redact.Secret
	}
	r.Info("leak", slog.Any("creds", creds{User: "alice", Token: "s3cr3t"}))
}
