// Бенчмарк: single vs bulk (разные размеры батча). OpenSearch 3.5.0, single-node, локально.
// Числа ИЛЛЮСТРАТИВНЫ — зависят от железа, сети, маппинга.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

func mkClient() *opensearchapi.Client {
	c, err := opensearchapi.NewClient(opensearchapi.Config{Client: opensearch.Config{
		Addresses: []string{"https://localhost:9214"}, Username: "admin", Password: "IngDemo#2026",
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}})
	if err != nil { log.Fatal(err) }
	return c
}

func doc(i int) string {
	return fmt.Sprintf(`{"ts":"2026-07-03T10:00:00Z","level":"info","service":"service-a","message":"msg %d","status":200}`, i)
}

func main() {
	c := mkClient(); ctx := context.Background()
	const N = 3000

	// single
	c.Indices.Delete(ctx, opensearchapi.IndicesDeleteReq{Indices: []string{"bench-single"}})
	t0 := time.Now()
	for i := 0; i < N; i++ {
		_, err := c.Index(ctx, opensearchapi.IndexReq{Index: "bench-single", Body: strings.NewReader(doc(i))})
		if err != nil { log.Fatal(err) }
	}
	dSingle := time.Since(t0)

	// bulk по batch
	for _, batch := range []int{500, 3000} {
		idx := fmt.Sprintf("bench-bulk-%d", batch)
		c.Indices.Delete(ctx, opensearchapi.IndicesDeleteReq{Indices: []string{idx}})
		t := time.Now()
		for start := 0; start < N; start += batch {
			var sb strings.Builder
			end := start + batch; if end > N { end = N }
			for i := start; i < end; i++ {
				sb.WriteString(fmt.Sprintf(`{"index":{"_index":%q}}`+"\n", idx))
				sb.WriteString(doc(i) + "\n")
			}
			if _, err := c.Bulk(ctx, opensearchapi.BulkReq{Body: strings.NewReader(sb.String())}); err != nil { log.Fatal(err) }
		}
		d := time.Since(t)
		fmt.Printf("bulk batch=%-4d : %8.0f docs/s (%v на %d док)\n", batch, float64(N)/d.Seconds(), d.Round(time.Millisecond), N)
	}
	fmt.Printf("single       : %8.0f docs/s (%v на %d док)\n", float64(N)/dSingle.Seconds(), dSingle.Round(time.Millisecond), N)
}
