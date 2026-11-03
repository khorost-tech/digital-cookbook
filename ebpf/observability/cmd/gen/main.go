// Генератор запросов — эталон стенда: отправляет заданное число запросов на
// каждый маршрут и печатает, сколько отправил и с каким статусом получил ответ.
// Трейсы Beyla и OTEL SDK сверяются с этим счётом.
//
//	gen TARGET          TARGET — хост сервиса (svc-plain или svc-sdk)
//	gen TARGET warmup   один запрос на /warmup — проверка, что инструментирование уже видит запросы
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// План нагрузки: маршрут → число запросов. /users/{id} — каждый раз новый id.
var plan = []struct {
	key, method, path string
	port              int
	n                 int
}{
	{"GET /users/{id}", "GET", "/users/%d", 8080, 300},
	{"POST /orders", "POST", "/orders?user=%d", 8080, 200},
	{"GET /fail", "GET", "/fail", 8080, 50},
	{"GET /health", "GET", "/health", 8080, 100},
	{"GET /v2/users/{id}", "GET", "/v2/users/%d", 8081, 300},
}

const grpcCalls = 150

func main() {
	host := os.Args[1]
	if len(os.Args) > 2 && os.Args[2] == "warmup" { // один запрос прогрева, без счёта
		resp, err := http.Get("http://" + host + ":8080/warmup")
		if err == nil {
			resp.Body.Close()
		}
		return
	}
	rnd := rand.New(rand.NewSource(20261008))
	sent := map[string]map[string]int{}
	ids := map[int]bool{}
	for _, p := range plan {
		sent[p.key] = map[string]int{}
		for i := 0; i < p.n; i++ {
			id := rnd.Intn(1_000_000)
			ids[id] = true
			path := p.path
			if p.key != "GET /fail" && p.key != "GET /health" {
				path = fmt.Sprintf(p.path, id)
			}
			req, _ := http.NewRequest(p.method, fmt.Sprintf("http://%s:%d%s", host, p.port, path), nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				sent[p.key]["error"]++
				continue
			}
			resp.Body.Close()
			sent[p.key][strconv.Itoa(resp.StatusCode)]++
		}
	}
	conn, err := grpc.NewClient(host+":9090", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	hc := healthpb.NewHealthClient(conn)
	g := map[string]int{}
	for i := 0; i < grpcCalls; i++ {
		if _, err := hc.Check(context.Background(), &healthpb.HealthCheckRequest{}); err != nil {
			g["error"]++
		} else {
			g["OK"]++
		}
	}
	sent["/grpc.health.v1.Health/Check"] = g
	b, _ := json.Marshal(map[string]any{"target": host, "sent": sent, "distinct_user_ids": len(ids)})
	fmt.Printf("RESULT %s\n", b)
}
