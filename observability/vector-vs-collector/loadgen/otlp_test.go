package main

import (
	"encoding/json"
	"regexp"
	"testing"
)

var (
	hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

func TestOTLPTracesPayloadHasRequestedSpans(t *testing.T) {
	body, err := BuildTracesPayload(5, 1)
	if err != nil {
		t.Fatalf("BuildTracesPayload: %v", err)
	}

	var got struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []struct {
					TraceID string `json:"traceId"`
					SpanID  string `json:"spanId"`
					Name    string `json:"name"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("тело не разбирается как JSON: %v", err)
	}

	if len(got.ResourceSpans) != 1 || len(got.ResourceSpans[0].ScopeSpans) != 1 {
		t.Fatalf("ожидалась одна пара resourceSpans/scopeSpans, получено %d/%d",
			len(got.ResourceSpans), len(got.ResourceSpans[0].ScopeSpans))
	}
	spans := got.ResourceSpans[0].ScopeSpans[0].Spans
	if len(spans) != 5 {
		t.Fatalf("ожидалось 5 спанов, получено %d", len(spans))
	}

	// Идентификаторы обязаны быть непустыми и правильной длины: приёмник OTLP
	// молча отбрасывает спаны с нулевым traceId, и тогда «отправили 100, дошло 0»
	// объяснялось бы свойствами приёмника, хотя дефект был бы в генераторе.
	seen := map[string]bool{}
	for i, s := range spans {
		if !hex32.MatchString(s.TraceID) {
			t.Errorf("спан %d: traceId=%q не 32 hex-символа", i, s.TraceID)
		}
		if !hex16.MatchString(s.SpanID) {
			t.Errorf("спан %d: spanId=%q не 16 hex-символов", i, s.SpanID)
		}
		if seen[s.SpanID] {
			t.Errorf("спан %d: spanId %s повторяется", i, s.SpanID)
		}
		seen[s.SpanID] = true
		if s.Name == "" {
			t.Errorf("спан %d: пустое имя", i)
		}
	}
}

func TestOTLPLogsPayloadHasRequestedRecords(t *testing.T) {
	body, err := BuildLogsPayload(3, 1)
	if err != nil {
		t.Fatalf("BuildLogsPayload: %v", err)
	}

	var got struct {
		ResourceLogs []struct {
			ScopeLogs []struct {
				LogRecords []struct {
					TimeUnixNano string `json:"timeUnixNano"`
					SeverityText string `json:"severityText"`
					Body         struct {
						StringValue string `json:"stringValue"`
					} `json:"body"`
				} `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("тело не разбирается как JSON: %v", err)
	}

	recs := got.ResourceLogs[0].ScopeLogs[0].LogRecords
	if len(recs) != 3 {
		t.Fatalf("ожидалось 3 записи, получено %d", len(recs))
	}
	for i, r := range recs {
		if r.TimeUnixNano == "" || r.TimeUnixNano == "0" {
			t.Errorf("запись %d: пустое время", i)
		}
		if r.Body.StringValue == "" {
			t.Errorf("запись %d: пустое тело", i)
		}
		if r.SeverityText == "" {
			t.Errorf("запись %d: пустой severityText", i)
		}
	}
}

func TestOTLPTracesPayloadIsDeterministicBySeed(t *testing.T) {
	// Один и тот же seed обязан давать одинаковые идентификаторы: иначе
	// повторный прогон замера нельзя сверить с предыдущим.
	a, err := BuildTracesPayload(4, 42)
	if err != nil {
		t.Fatalf("BuildTracesPayload: %v", err)
	}
	b, err := BuildTracesPayload(4, 42)
	if err != nil {
		t.Fatalf("BuildTracesPayload: %v", err)
	}
	if string(a) != string(b) {
		t.Error("при одинаковом seed тела запросов различаются")
	}
}
