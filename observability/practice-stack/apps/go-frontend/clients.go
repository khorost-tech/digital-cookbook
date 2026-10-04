package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.30.0"
	"go.opentelemetry.io/otel/trace"
)

// httpInventory — клиент к Java-бэкенду. Пока это обычный http.Client без
// всякой телеметрии: в статье 2 именно здесь показывается, что запрос,
// отправленный таким клиентом, рвёт трейс — контекст никуда не попадает.
type httpInventory struct {
	baseURL string
	client  *http.Client
}

func newHTTPInventory(baseURL string, timeout time.Duration) *httpInventory {
	return &httpInventory{
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
	}
}

// newInstrumentedHTTPInventory отличается от newHTTPInventory ровно одной
// строкой — обёрткой транспорта. Она делает две вещи разом: создаёт клиентский
// спан и инъектирует traceparent в заголовки запроса. Без неё трейс рвётся на
// границе сервисов, и в ст. 2 это показано отдельным опытом.
func newInstrumentedHTTPInventory(baseURL string, timeout time.Duration) *httpInventory {
	return &httpInventory{
		baseURL: baseURL,
		client: &http.Client{
			Timeout:   timeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

func (h *httpInventory) Lookup(ctx context.Context, sku string) (Item, error) {
	url := fmt.Sprintf("%s/inventory/%s", h.baseURL, sku)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Item{}, err
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return Item{}, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		var item Item
		if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
			return Item{}, fmt.Errorf("%w: разбор ответа: %v", ErrUpstream, err)
		}
		return item, nil
	case http.StatusNotFound:
		return Item{}, ErrNotFound
	default:
		return Item{}, fmt.Errorf("%w: код %d", ErrUpstream, resp.StatusCode)
	}
}

// natsHeaderCarrier переносит trace context в заголовки сообщения NATS. Для HTTP
// такой переносчик есть в готовом виде (propagation.HeaderCarrier), для брокеров
// его пишут руками: заголовки у каждого свои.
type natsHeaderCarrier nats.Header

func (c natsHeaderCarrier) Get(key string) string {
	return nats.Header(c).Get(key)
}

func (c natsHeaderCarrier) Set(key, value string) {
	nats.Header(c).Set(key, value)
}

func (c natsHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// natsPublisher публикует событие о созданном заказе и переносит trace context в
// заголовки сообщения. Асинхронная граница — то место, где трейс рвётся сам
// собой: у сообщения нет ничего похожего на HTTP-заголовки по умолчанию, и
// подписчик начинает новый трейс, не связанный с заказом.
type natsPublisher struct {
	conn *nats.Conn
	// propagate=false воспроизводит именно этот разрыв: сообщение уходит без
	// заголовков, и в Tempo появляются два несвязанных трейса вместо одного.
	propagate bool
}

func (p *natsPublisher) Publish(ctx context.Context, subject string, payload []byte) error {
	// Спан вида PRODUCER: по семантическим конвенциям публикация сообщения — это
	// отдельная операция, а не часть родительского спана.
	ctx, span := otel.Tracer("go-frontend/nats").Start(ctx,
		subject+" publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			semconv.MessagingSystemKey.String("nats"),
			semconv.MessagingDestinationName(subject),
			attribute.Int("messaging.message.body.size", len(payload)),
			attribute.Bool("stand.propagate_context", p.propagate),
		),
	)
	defer span.End()

	msg := nats.NewMsg(subject)
	msg.Data = payload
	if p.propagate {
		otel.GetTextMapPropagator().Inject(ctx, natsHeaderCarrier(msg.Header))
	}

	if err := p.conn.PublishMsg(msg); err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return err
	}
	return nil
}

// noopPublisher нужен на случай, когда NATS в стенде не поднят: сервис должен
// остаться работоспособным, а не падать при старте.
type noopPublisher struct{}

func (noopPublisher) Publish(context.Context, string, []byte) error { return nil }
