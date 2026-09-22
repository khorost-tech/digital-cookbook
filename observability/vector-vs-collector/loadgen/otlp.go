package main

// OTLP-режим генератора: отправка трейсов и логов по OTLP/HTTP.
//
// Тела запросов собираются вручную в JSON-кодировании OTLP, без SDK
// OpenTelemetry. Причина не в экономии зависимостей, а в назначении стенда:
// проверяется, что именно приёмник делает с полученным сигналом, и для этого
// нужно точно знать, что ушло на провод. SDK прячет тело за слоями и мешает
// отвечать на вопрос «приёмник потерял данные или генератор их не отправил».
//
// OTLP/HTTP допускает и protobuf, и JSON (Content-Type: application/json).
// Оба приёмника стенда — Collector и Vector — принимают JSON-вариант.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"
)

// Базовое время тел запросов. Константа, а не time.Now(): при одинаковом seed
// тела обязаны совпадать байт в байт, иначе повторный прогон замера не с чем
// сверить. Для проверки маршрутизации сигналов абсолютное время роли не играет.
var otlpBaseTime = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

type otlpAttr struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
	} `json:"value"`
}

func attr(k, v string) otlpAttr {
	var a otlpAttr
	a.Key = k
	a.Value.StringValue = v
	return a
}

type otlpResource struct {
	Attributes []otlpAttr `json:"attributes"`
}

type otlpScope struct {
	Name string `json:"name"`
}

type otlpSpan struct {
	TraceID           string     `json:"traceId"`
	SpanID            string     `json:"spanId"`
	Name              string     `json:"name"`
	Kind              int        `json:"kind"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	EndTimeUnixNano   string     `json:"endTimeUnixNano"`
	Attributes        []otlpAttr `json:"attributes"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpTracesRequest struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpBody struct {
	StringValue string `json:"stringValue"`
}

type otlpLogRecord struct {
	TimeUnixNano   string     `json:"timeUnixNano"`
	SeverityNumber int        `json:"severityNumber"`
	SeverityText   string     `json:"severityText"`
	Body           otlpBody   `json:"body"`
	Attributes     []otlpAttr `json:"attributes"`
	TraceID        string     `json:"traceId,omitempty"`
	SpanID         string     `json:"spanId,omitempty"`
}

type otlpScopeLogs struct {
	Scope      otlpScope       `json:"scope"`
	LogRecords []otlpLogRecord `json:"logRecords"`
}

type otlpResourceLogs struct {
	Resource  otlpResource    `json:"resource"`
	ScopeLogs []otlpScopeLogs `json:"scopeLogs"`
}

type otlpLogsRequest struct {
	ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
}

func resourceOf() otlpResource {
	return otlpResource{Attributes: []otlpAttr{
		attr("service.name", "loadgen"),
		attr("deployment.environment", "vector-vs-collector"),
	}}
}

// randHex возвращает n случайных байт в hex — идентификаторы OTLP в
// JSON-кодировании передаются именно так.
func randHex(r *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Intn(256))
	}
	return hex.EncodeToString(b)
}

// BuildTracesPayload собирает тело запроса OTLP/HTTP с count спанами.
func BuildTracesPayload(count int, seed int64) ([]byte, error) {
	if count <= 0 {
		return nil, fmt.Errorf("count должен быть больше нуля, получено %d", count)
	}
	r := rand.New(rand.NewSource(seed))
	ops := []string{"GET /api/items", "POST /api/orders", "db.query", "cache.get"}

	spans := make([]otlpSpan, 0, count)
	for i := 0; i < count; i++ {
		start := otlpBaseTime.Add(time.Duration(i) * time.Millisecond)
		end := start.Add(time.Duration(1+r.Intn(50)) * time.Millisecond)
		spans = append(spans, otlpSpan{
			TraceID:           randHex(r, 16),
			SpanID:            randHex(r, 8),
			Name:              ops[r.Intn(len(ops))],
			Kind:              2, // SPAN_KIND_SERVER
			StartTimeUnixNano: fmt.Sprintf("%d", start.UnixNano()),
			EndTimeUnixNano:   fmt.Sprintf("%d", end.UnixNano()),
			Attributes:        []otlpAttr{attr("http.method", "GET"), attr("pipeline", "loadgen")},
		})
	}

	return json.Marshal(otlpTracesRequest{ResourceSpans: []otlpResourceSpans{{
		Resource:   resourceOf(),
		ScopeSpans: []otlpScopeSpans{{Scope: otlpScope{Name: "loadgen"}, Spans: spans}},
	}}})
}

// BuildLogsPayload собирает тело запроса OTLP/HTTP с count записями логов.
func BuildLogsPayload(count int, seed int64) ([]byte, error) {
	if count <= 0 {
		return nil, fmt.Errorf("count должен быть больше нуля, получено %d", count)
	}
	r := rand.New(rand.NewSource(seed))
	sev := []struct {
		num  int
		text string
	}{{9, "INFO"}, {13, "WARN"}, {17, "ERROR"}}

	recs := make([]otlpLogRecord, 0, count)
	for i := 0; i < count; i++ {
		s := sev[r.Intn(len(sev))]
		ts := otlpBaseTime.Add(time.Duration(i) * time.Millisecond)
		recs = append(recs, otlpLogRecord{
			TimeUnixNano:   fmt.Sprintf("%d", ts.UnixNano()),
			SeverityNumber: s.num,
			SeverityText:   s.text,
			Body:           otlpBody{StringValue: messages[r.Intn(len(messages))]},
			Attributes:     []otlpAttr{attr("service", "api"), attr("pipeline", "loadgen")},
			// Идентификаторы связи с трейсом заполняются намеренно: по ним видно,
			// сохраняет ли приёмник корреляцию лога со спаном.
			TraceID: randHex(r, 16),
			SpanID:  randHex(r, 8),
		})
	}

	return json.Marshal(otlpLogsRequest{ResourceLogs: []otlpResourceLogs{{
		Resource:  resourceOf(),
		ScopeLogs: []otlpScopeLogs{{Scope: otlpScope{Name: "loadgen"}, LogRecords: recs}},
	}}})
}

// runOTLP отправляет трейсы и логи и печатает по каждому сигналу код ответа
// и тело. Печатается ВСЁ, включая успешные ответы: приёмник может вернуть 200
// и сообщить о частичном отказе в теле, и без вывода это выглядело бы как
// успешная доставка.
func runOTLP(endpoint string, spans, logRecords int, seed int64) {
	if spans > 0 {
		body, err := BuildTracesPayload(spans, seed)
		if err != nil {
			fmt.Printf("трейсы: не собрать тело: %v\n", err)
		} else {
			code, resp, err := SendOTLP(endpoint, "/v1/traces", body)
			if err != nil {
				fmt.Printf("трейсы: отправка не удалась: %v\n", err)
			} else {
				fmt.Printf("трейсы: отправлено %d спанов (%d байт) → HTTP %d, ответ: %s\n",
					spans, len(body), code, resp)
			}
		}
	}

	if logRecords > 0 {
		body, err := BuildLogsPayload(logRecords, seed)
		if err != nil {
			fmt.Printf("логи: не собрать тело: %v\n", err)
		} else {
			code, resp, err := SendOTLP(endpoint, "/v1/logs", body)
			if err != nil {
				fmt.Printf("логи: отправка не удалась: %v\n", err)
			} else {
				fmt.Printf("логи: отправлено %d записей (%d байт) → HTTP %d, ответ: %s\n",
					logRecords, len(body), code, resp)
			}
		}
	}
}

// SendOTLP отправляет тело на приёмник OTLP/HTTP и возвращает код ответа и
// его тело. Ответ возвращается ЦЕЛИКОМ и печатается вызывающим: приёмник может
// ответить 200 и при этом сообщить о частичном отказе в теле (partialSuccess),
// и отличить «принято» от «принято, но отброшено» можно только так.
func SendOTLP(endpoint, path string, body []byte) (int, string, error) {
	url := endpoint + path
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(respBody), nil
}
