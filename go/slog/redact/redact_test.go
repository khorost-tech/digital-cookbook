package redact

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// logJSON пишет одну запись в буфер JSON-хендлером и возвращает разобранные поля.
func logJSON(t *testing.T, opts *slog.HandlerOptions, args ...any) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, opts)).Info("msg", args...)
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("невалидный JSON записи: %v\n%s", err, buf.String())
	}
	return m
}

func TestSecretLogValuer(t *testing.T) {
	m := logJSON(t, nil, slog.Any("token", Secret("s3cr3t-abcdef")))
	if got := m["token"]; got != "REDACTED" {
		t.Errorf("token: ждали REDACTED, получили %v", got)
	}
	// Секрет физически не попал в вывод.
	if b, _ := json.Marshal(m); strings.Contains(string(b), "s3cr3t") {
		t.Errorf("секрет утёк в вывод: %s", b)
	}
}

func TestCardLastFour(t *testing.T) {
	m := logJSON(t, nil, slog.Any("card", Card("4111111111111234")))
	if got := m["card"]; got != "**** **** **** 1234" {
		t.Errorf("card: ждали хвост 1234, получили %v", got)
	}
	if got := logJSON(t, nil, slog.Any("card", Card("12")))["card"]; got != "****" {
		t.Errorf("короткая card: ждали ****, получили %v", got)
	}
}

func TestRedactKeysReplaceAttr(t *testing.T) {
	opts := &slog.HandlerOptions{ReplaceAttr: RedactKeys("password", "authorization")}
	m := logJSON(t, opts,
		slog.String("user", "alice"),
		slog.String("password", "hunter2"),
		slog.String("authorization", "Bearer xyz"),
	)
	if m["password"] != "***" || m["authorization"] != "***" {
		t.Errorf("ключи не замаскированы: %v", m)
	}
	if m["user"] != "alice" {
		t.Errorf("незасекреченное поле не должно меняться: %v", m["user"])
	}
	if b, _ := json.Marshal(m); strings.Contains(string(b), "hunter2") || strings.Contains(string(b), "Bearer") {
		t.Errorf("секрет утёк несмотря на ReplaceAttr: %s", b)
	}
}

// TestBoundaryStructLeaks фиксирует ЧЕСТНУЮ границу LogValuer. Поле Token имеет
// тип Secret — то есть КАК САМОСТОЯТЕЛЬНЫЙ атрибут оно замаскировалось бы в
// "REDACTED" (это проверяет TestSecretLogValuer). Но здесь Secret спрятан полем
// внутри структуры, отданной в slog.Any: JSON-кодировщик хендлера сериализует
// структуру целиком и НЕ зовёт LogValue её полей, а ReplaceAttr по ключу "Token"
// видит только внешний атрибут "creds" — вложенного ключа в его поле зрения нет.
// Поэтому сырой секрет утекает. Тест закрепляет это как ожидаемое поведение
// (граница способа), а не баг: сравните с TestSecretLogValuer, где тот же тип
// Secret как отдельный атрибут маскируется.
func TestBoundaryStructLeaks(t *testing.T) {
	type creds struct {
		User  string
		Token Secret // тот же LogValuer-тип, что маскируется как отдельный атрибут
	}
	opts := &slog.HandlerOptions{ReplaceAttr: RedactKeys("Token")}
	m := logJSON(t, opts, slog.Any("creds", creds{User: "alice", Token: "s3cr3t"}))
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), "s3cr3t") {
		t.Fatalf("ожидали УТЕЧКУ Secret-поля внутри структуры (граница LogValuer), но её нет: %s", b)
	}
	if strings.Contains(string(b), "REDACTED") {
		t.Fatalf("LogValue поля не должен вызываться внутри slog.Any(struct), но маска появилась: %s", b)
	}
}
