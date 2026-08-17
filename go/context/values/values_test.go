package values

import (
	"context"
	"testing"
)

func TestRequestIDRoundTrip(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-42")
	id, ok := RequestID(ctx)
	if !ok || id != "req-42" {
		t.Fatalf("ждали req-42, получили %q ok=%v", id, ok)
	}
}

func TestMissingValue(t *testing.T) {
	// на пустом контексте значения нет — геттер обязан вернуть ok=false,
	// а не паниковать
	if _, ok := RequestID(context.Background()); ok {
		t.Fatal("на пустом контексте не должно быть requestID")
	}
}

func TestTypedKeysDoNotCollide(t *testing.T) {
	// два разных ключа (requestID и userID) не пересекаются, хотя оба
	// имеют базовый тип int
	ctx := WithRequestID(context.Background(), "req-1")
	ctx = WithUserID(ctx, 777)

	if id, ok := RequestID(ctx); !ok || id != "req-1" {
		t.Fatalf("requestID затёрт: %q ok=%v", id, ok)
	}
	if uid, ok := UserID(ctx); !ok || uid != 777 {
		t.Fatalf("userID затёрт: %d ok=%v", uid, ok)
	}
}

func TestChildOverridesValue(t *testing.T) {
	// дочерний контекст перекрывает значение, не трогая родительский
	parent := WithRequestID(context.Background(), "outer")
	child := WithRequestID(parent, "inner")

	if id, _ := RequestID(child); id != "inner" {
		t.Fatalf("дочерний контекст должен видеть inner, видит %q", id)
	}
	if id, _ := RequestID(parent); id != "outer" {
		t.Fatalf("родительский контекст изменился на %q", id)
	}
}
