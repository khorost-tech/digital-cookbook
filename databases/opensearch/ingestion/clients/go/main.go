// Демо opensearch-go v4: single index + bulk + разбор partial failures.
// Проверено на OpenSearch 3.5.0. demo-креды — только для локального стенда.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

type LogDoc struct {
	Ts      string `json:"ts"`
	Level   string `json:"level"`
	Service string `json:"service"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func main() {
	// По умолчанию — localhost (клиент запускается на хосте рядом со стендом). Для запуска
	// ИЗ контейнера, которому нужен хостовый стенд, задайте OS_HOST=host.docker.internal.
	osHost := os.Getenv("OS_HOST")
	if osHost == "" {
		osHost = "localhost"
	}
	client, err := opensearchapi.NewClient(opensearchapi.Config{
		Client: opensearch.Config{
			Addresses: []string{fmt.Sprintf("https://%s:9214", osHost)},
			Username:  "admin",
			Password:  "IngDemo#2026",
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // demo only
			},
		},
	})
	if err != nil {
		log.Fatalf("client: %v", err)
	}
	ctx := context.Background()

	// --- single index ---
	body := `{"ts":"2026-07-03T10:00:00Z","level":"error","service":"service-a","message":"disk full","status":500}`
	ir, err := client.Index(ctx, opensearchapi.IndexReq{
		Index: "app-logs-go",
		Body:  strings.NewReader(body),
	})
	if err != nil {
		log.Fatalf("index: %v", err)
	}
	fmt.Printf("single index -> result=%s id=%s\n", ir.Result, ir.ID)

	// --- bulk (NDJSON: action-строка + документ-строка) ---
	var sb strings.Builder
	docs := []LogDoc{
		{Ts: "2026-07-03T10:01:00Z", Level: "info", Service: "service-a", Message: "ok", Status: 200},
		{Ts: "2026-07-03T10:02:00Z", Level: "warn", Service: "service-b", Message: "retry", Status: 429},
	}
	for _, d := range docs {
		sb.WriteString(`{"index":{"_index":"app-logs-go"}}` + "\n")
		sb.WriteString(fmt.Sprintf(`{"ts":%q,"level":%q,"service":%q,"message":%q,"status":%d}`+"\n",
			d.Ts, d.Level, d.Service, d.Message, d.Status))
	}
	br, err := client.Bulk(ctx, opensearchapi.BulkReq{Body: strings.NewReader(sb.String())})
	if err != nil {
		log.Fatalf("bulk: %v", err)
	}
	// ВАЖНО: HTTP-успех != все документы записаны. Проверяем каждый item.
	fmt.Printf("bulk -> errors=%v, items=%d\n", br.Errors, len(br.Items))
	for i, item := range br.Items {
		op := item["index"]
		if op.Status >= 300 {
			fmt.Printf("  item %d FAILED status=%d type=%s\n", i, op.Status, op.Error.Type)
		} else {
			fmt.Printf("  item %d ok status=%d\n", i, op.Status)
		}
	}
}
