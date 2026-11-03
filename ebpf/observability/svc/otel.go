//go:build otel

// Инструментирование OTEL SDK: спаны HTTP-сервера с шаблоном маршрута,
// gRPC-сервера и SQL-запросов, экспорт OTLP в коллектор стенда.
package main

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/XSAM/otelsql"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"google.golang.org/grpc"
)

func init() {
	instrumentInit = func(ctx context.Context) func() {
		exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithInsecure())
		if err != nil {
			panic(err)
		}
		tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp))
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(propagation.TraceContext{})
		return func() { tp.Shutdown(context.Background()) }
	}
	instrumentHandler = func(route string, h http.Handler) http.Handler {
		return otelhttp.NewHandler(h, route, otelhttp.WithSpanNameFormatter(
			func(_ string, r *http.Request) string { return r.Method + " " + route }))
	}
	instrumentDB = func(dsn string) (*sql.DB, error) {
		return otelsql.Open("pgx", dsn, otelsql.WithAttributes(semconv.DBSystemPostgreSQL))
	}
	instrumentGRPC = func() []grpc.ServerOption {
		return []grpc.ServerOption{grpc.StatsHandler(otelgrpc.NewServerHandler())}
	}
}
