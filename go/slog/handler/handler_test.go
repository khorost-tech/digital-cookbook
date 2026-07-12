package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"log/slog"
)

func TestContextHandlerInjectsRequestID(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(New(slog.NewJSONHandler(&buf, nil)))

	ctx := WithRequestID(context.Background(), "req-99")
	l.InfoContext(ctx, "работаем")

	m := map[string]any{}
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("невалидный JSON: %v", err)
	}
	if m["request_id"] != "req-99" {
		t.Fatalf("хендлер не подставил request_id из контекста: %v", m)
	}
}

func TestContextHandlerNoIDWhenAbsent(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(New(slog.NewJSONHandler(&buf, nil)))

	l.InfoContext(context.Background(), "без id")

	m := map[string]any{}
	_ = json.Unmarshal(buf.Bytes(), &m)
	if _, ok := m["request_id"]; ok {
		t.Fatal("request_id появился, хотя в контексте его не было")
	}
}

func TestContextHandlerKeepsDecoratorThroughWith(t *testing.T) {
	// после logger.With(...) декоратор должен сохраниться и по-прежнему
	// подставлять request_id — это проверяет корректность WithAttrs
	var buf bytes.Buffer
	base := slog.New(New(slog.NewJSONHandler(&buf, nil)))
	l := base.With(slog.String("service", "api"))

	ctx := WithRequestID(context.Background(), "req-abc")
	l.InfoContext(ctx, "после With")

	m := map[string]any{}
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("невалидный JSON: %v", err)
	}
	if m["service"] != "api" {
		t.Errorf("потеряли постоянный атрибут service: %v", m)
	}
	if m["request_id"] != "req-abc" {
		t.Errorf("декоратор потерялся после With — нет request_id: %v", m)
	}
}
