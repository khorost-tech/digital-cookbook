package basic

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"log/slog"
)

// decode читает одну JSON-строку лога в map для проверки полей.
func decode(t *testing.T, line []byte) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("невалидный JSON лога: %v\n%s", err, line)
	}
	return m
}

func TestJSONHasStructuredFields(t *testing.T) {
	var buf bytes.Buffer
	l := JSONLogger(&buf, slog.LevelInfo)
	LogRequest(l, "GET", "/users/42", 200)

	m := decode(t, buf.Bytes())
	if m["msg"] != "http request" {
		t.Errorf("msg = %v", m["msg"])
	}
	if m["method"] != "GET" || m["path"] != "/users/42" {
		t.Errorf("method/path = %v %v", m["method"], m["path"])
	}
	if m["status"].(float64) != 200 {
		t.Errorf("status = %v", m["status"])
	}
	if m["level"] != "INFO" {
		t.Errorf("level = %v", m["level"])
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l := JSONLogger(&buf, slog.LevelWarn) // Info ниже порога — не должен попасть
	l.Info("тихо")
	l.Warn("громко")

	out := buf.String()
	if strings.Contains(out, "тихо") {
		t.Error("Info просочился при уровне Warn")
	}
	if !strings.Contains(out, "громко") {
		t.Error("Warn не записан")
	}
}

func TestWithAddsFieldToEveryRecord(t *testing.T) {
	var buf bytes.Buffer
	l := WithRequestID(JSONLogger(&buf, slog.LevelInfo), "req-7")
	l.Info("первая")
	l.Info("вторая")

	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if decode(t, line)["request_id"] != "req-7" {
			t.Errorf("request_id отсутствует в записи: %s", line)
		}
	}
}

func TestGroupNests(t *testing.T) {
	var buf bytes.Buffer
	l := JSONLogger(&buf, slog.LevelInfo)
	LogInGroup(l, "SELECT 1", 3)

	m := decode(t, buf.Bytes())
	db, ok := m["db"].(map[string]any)
	if !ok {
		t.Fatalf("группа db не стала вложенным объектом: %v", m["db"])
	}
	if db["query"] != "SELECT 1" || db["rows"].(float64) != 3 {
		t.Errorf("поля группы: %v", db)
	}
}

func TestLogFastSameOutput(t *testing.T) {
	var buf bytes.Buffer
	l := JSONLogger(&buf, slog.LevelInfo)
	LogFast(context.Background(), l, "/health", 204)

	m := decode(t, buf.Bytes())
	if m["path"] != "/health" || m["status"].(float64) != 204 {
		t.Errorf("LogAttrs дал другой результат: %v", m)
	}
}
