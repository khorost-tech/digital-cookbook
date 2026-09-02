package registry

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Фейковый Apicurio v3: формы запросов и ответов сняты живым контейнером
// apicurio/apicurio-registry:latest (SQL/H2, см. bench/run-need-schema.sh)
// вручную через curl перед тем, как писать этот код — а не придуманы по
// памяти. Ровно это здесь и проверяется: клиент обязан посылать запрос в
// той форме, которую реестр реально принимает, и разбирать ответ в той
// форме, которую он реально отдаёт.
func fakeApicurio(t *testing.T, compatible bool) *httptest.Server {
	t.Helper()
	nextGlobalID := int64(1)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/apis/registry/v3/groups/default/artifacts":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			id := nextGlobalID
			nextGlobalID++
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"artifact": map[string]any{"artifactId": body["artifactId"]},
				"version":  map[string]any{"globalId": id, "version": "1"},
			})
		case r.Method == http.MethodPost && filepathHasSuffix(r.URL.Path, "/rules"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && filepathHasSuffix(r.URL.Path, "/versions"):
			if compatible {
				id := nextGlobalID
				nextGlobalID++
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{"globalId": id, "version": "2"})
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 400, "name": "RuleViolationException", "detail": "несовместимо",
			})
		case r.Method == http.MethodGet && filepathHasSuffix(r.URL.Path, "/ids/globalIds/1"):
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"type":"record","name":"User","fields":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func filepathHasSuffix(p, suffix string) bool {
	if len(p) < len(suffix) {
		return false
	}
	return p[len(p)-len(suffix):] == suffix
}

func TestCreateArtifact_ParsesGlobalID(t *testing.T) {
	srv := fakeApicurio(t, true)
	defer srv.Close()
	c := New(srv.URL)

	globalID, status, err := c.CreateArtifact("default", "user-need-test", "AVRO", `{"type":"record"}`, "application/json")
	if err != nil {
		t.Fatalf("CreateArtifact: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, хотим 200", status)
	}
	if globalID != 1 {
		t.Fatalf("globalID = %d, хотим 1 (из тела ответа фейкового реестра)", globalID)
	}
	if c.Calls() != 1 {
		t.Fatalf("Calls() = %d, хотим ровно 1 обращение", c.Calls())
	}
}

func TestSetCompatibilityRule_204(t *testing.T) {
	srv := fakeApicurio(t, true)
	defer srv.Close()
	c := New(srv.URL)

	status, err := c.SetCompatibilityRule("default", "user-need-test", "BACKWARD")
	if err != nil {
		t.Fatalf("SetCompatibilityRule: %v", err)
	}
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, хотим 204", status)
	}
}

func TestAddVersion_AcceptedAndRejected(t *testing.T) {
	for _, compatible := range []bool{true, false} {
		srv := fakeApicurio(t, compatible)
		c := New(srv.URL)
		_, status, body, err := c.AddVersion("default", "user-need-test", `{"type":"record"}`, "application/json")
		srv.Close()
		if err != nil {
			t.Fatalf("AddVersion(compatible=%v): %v", compatible, err)
		}
		wantAccepted := compatible
		gotAccepted := status >= 200 && status < 300
		if gotAccepted != wantAccepted {
			t.Fatalf("compatible=%v: status=%d body=%q — принятие не совпало с ожиданием", compatible, status, body)
		}
		if !compatible && body == "" {
			t.Fatalf("compatible=false: тело диагностики пустое — отказ реестра ничем не подтверждён")
		}
	}
}

func TestFetchByGlobalID(t *testing.T) {
	srv := fakeApicurio(t, true)
	defer srv.Close()
	c := New(srv.URL)

	content, status, err := c.FetchByGlobalID(1)
	if err != nil {
		t.Fatalf("FetchByGlobalID: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, хотим 200", status)
	}
	if content == "" {
		t.Fatalf("пустое содержимое схемы")
	}
	if c.Calls() != 1 {
		t.Fatalf("Calls() = %d — счётчик обращений обязан считать РОВНО этот один HTTP-вызов (Задача 7, требование 4: сколько обращений к реестру нужно до первого чтения)", c.Calls())
	}
}

// TestFetchByGlobalID_Unreachable — реестр недоступен (сервер уже закрыт).
// Это тот самый путь, которым доказывается недоступностью требование
// «Avro без схемы писателя не читается»: попытка обратиться к погашенному
// реестру обязана вернуться ОШИБКОЙ ТРАНСПОРТА, а не как-то иначе
// раздобыть схему.
func TestFetchByGlobalID_Unreachable(t *testing.T) {
	srv := fakeApicurio(t, true)
	srv.Close() // гасим ДО обращения
	c := New(srv.URL)

	_, _, err := c.FetchByGlobalID(1)
	if err == nil {
		t.Fatalf("ожидали ошибку обращения к погашенному реестру, получили nil")
	}
}
