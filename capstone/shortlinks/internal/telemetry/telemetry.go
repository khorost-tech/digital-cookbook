// Package telemetry — трейсы OpenTelemetry. Без OTEL_EXPORTER_OTLP_ENDPOINT провайдер не
// ставится, и спаны ничего не стоят. Контекст между сервисом и consumer'ом едет в заголовках
// сообщения NATS (W3C traceparent) — иначе трейс обрывается на брокере.
package telemetry

import (
	"context"
	"net/http"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

var (
	Propagate = true  // PROPAGATE=false в опыте: без проброса контекста через брокер
	Canonical = false // NATS_HEADER_CARRIER=canonical: http.Header-перевозчик, имена приводятся к Traceparent
)

// natsCarrier — имена заголовков как есть: заголовки NATS чувствительны к регистру, а получатели
// на других языках (jnats, Java-агент OpenTelemetry) ищут ровно "traceparent".
type natsCarrier nats.Header

func (c natsCarrier) Get(k string) string {
	if v := c[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}
func (c natsCarrier) Set(k, v string) { c[k] = []string{v} }
func (c natsCarrier) Keys() []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	return out
}

func Setup(ctx context.Context, service, endpoint string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint+"/v1/traces"))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(service))))
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

func Tracer() trace.Tracer { return otel.Tracer("shortlinks") }

func HTTP(h http.Handler) http.Handler {
	// r.Pattern уже содержит метод: "GET /{code}"
	return otelhttp.NewHandler(h, "http", otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
		if r.Pattern == "" {
			return r.Method
		}
		return r.Pattern
	}))
}

func carrier(h nats.Header) propagation.TextMapCarrier {
	if Canonical {
		return propagation.HeaderCarrier(h)
	}
	return natsCarrier(h)
}

func InjectNATS(ctx context.Context, h nats.Header) {
	if Propagate {
		otel.GetTextMapPropagator().Inject(ctx, carrier(h))
	}
}

func ExtractNATS(ctx context.Context, h nats.Header) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, carrier(h))
}
