// Тестовый сервис стенда: HTTP (маршрут с параметром в пути, запись в
// PostgreSQL, ответ 500), gRPC (health) и PostgreSQL. Собирается дважды:
// без инструментирования (go build) и с OTEL SDK (go build -tags otel) —
// инструментирование подключает otel.go, логика одна.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// instrument* заменяет otel.go при сборке с тегом otel; без него — пустые обёртки.
var (
	instrumentInit    = func(ctx context.Context) func() { return func() {} }
	instrumentHandler = func(route string, h http.Handler) http.Handler { return h }
	instrumentDB      = func(dsn string) (*sql.DB, error) { return sql.Open("pgx", dsn) }
	instrumentGRPC    = func() []grpc.ServerOption { return nil }
)

func main() {
	shutdown := instrumentInit(context.Background())
	defer shutdown()
	db, err := instrumentDB(os.Getenv("STAND_DSN"))
	if err != nil {
		log.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS orders (id serial PRIMARY KEY, user_id int)"); err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	handle := func(pattern, route string, f http.HandlerFunc) {
		mux.Handle(pattern, instrumentHandler(route, f))
	}
	handle("GET /users/{id}", "/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		var n int
		if err := db.QueryRowContext(r.Context(), "SELECT count(*) FROM orders WHERE user_id = $1", id).Scan(&n); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		fmt.Fprintf(w, "user %d orders %d\n", id, n)
	})
	handle("POST /orders", "/orders", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.URL.Query().Get("user"))
		if _, err := db.ExecContext(r.Context(), "INSERT INTO orders (user_id) VALUES ($1)", id); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	handle("GET /fail", "/fail", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "failure on purpose", http.StatusInternalServerError)
	})
	handle("GET /health", "/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	// прогрев: раннер шлёт сюда, пока спан не появится в выгрузке, — в счёт не входит
	handle("GET /warmup", "/warmup", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	gs := grpc.NewServer(instrumentGRPC()...)
	healthpb.RegisterHealthServer(gs, health.NewServer())
	lis, err := net.Listen("tcp", ":9090")
	if err != nil {
		log.Fatal(err)
	}
	go gs.Serve(lis)

	// :8081 — путь разбирает сам обработчик, без шаблона ServeMux: так устроены
	// самописные роутеры и старый код. Маршрут знает только программист.
	legacy := instrumentHandler("/v2/users/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/v2/users/"))
		if err != nil || !strings.HasPrefix(r.URL.Path, "/v2/users/") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, "user %d\n", id)
	}))
	go func() { log.Fatal(http.ListenAndServe(":8081", legacy)) }()
	log.Fatal(http.ListenAndServe(":8080", mux))
}
