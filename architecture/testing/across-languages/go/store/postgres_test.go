//go:build integration

package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"tech.khorost/across-languages-go/service"
)

// ПРИЁМ 4: Testcontainers — общий знаменатель трёх экосистем. Настоящий
// Postgres в контейнере вместо фейка: проверяем то, чего фейк не знает, — SQL,
// схему, ограничения БД.
//
//	go test -tags=integration ./store/
//
// Контейнер поднимается ОДИН на весь пакет (как в Java- и Python-частях): его
// старт стоит секунды, а сам тест — миллисекунды. Изоляция тестов — не
// пересозданием контейнера, а TRUNCATE перед каждым.

var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	ctx := context.Background()

	pg, err := tcpg.Run(ctx, "postgres:18.1-alpine",
		tcpg.WithDatabase("orders"),
		tcpg.WithUsername("test"),
		tcpg.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "старт контейнера Postgres: %v\n", err)
		os.Exit(1)
	}

	code := run(ctx, pg, m)
	_ = testcontainers.TerminateContainer(pg)
	os.Exit(code)
}

// run отделён от TestMain, чтобы defer'ы отработали до os.Exit.
func run(ctx context.Context, pg *tcpg.PostgresContainer, m *testing.M) int {
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "строка подключения: %v\n", err)
		return 1
	}
	pool, err = pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "пул: %v\n", err)
		return 1
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, Schema); err != nil {
		fmt.Fprintf(os.Stderr, "схема: %v\n", err)
		return 1
	}
	return m.Run()
}

// cleanDB — изоляция: каждый тест начинает с чистого листа.
func cleanDB(t *testing.T) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "TRUNCATE orders"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
}

// Сервис целиком: домен + настоящее хранилище. Фейк подтвердил бы логику
// скидки, но не то, что она доедет до диска и вернётся обратно.
func TestИнтеграция_ЗаказДоезжаетДоPostgres(t *testing.T) {
	cleanDB(t)
	ctx := context.Background()
	st := Postgres{Pool: pool}
	svc := service.OrderService{Store: st, Notifier: nopNotifier{}}

	if _, err := svc.Create(ctx, "o-1", "u-1", 25_000); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := st.ByUser(ctx, "u-1")
	if err != nil {
		t.Fatalf("ByUser: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ожидали 1 заказ, получили %d", len(got))
	}
	if got[0].PriceCent != 22_500 {
		t.Errorf("к оплате %d, ожидали 22500 (250.00 минус 10%%)", got[0].PriceCent)
	}
}

// А вот это фейк из service/doubles_test.go не проверяет вовсе: у него нет
// первичного ключа, и повторный Save он молча примет. Настоящая БД — нет.
func TestИнтеграция_ДубльIDОтклоняетсяБазой(t *testing.T) {
	cleanDB(t)
	ctx := context.Background()
	st := Postgres{Pool: pool}
	o := service.Order{ID: "o-1", UserID: "u-1", TotalCent: 10_000, PriceCent: 9_500}

	if err := st.Save(ctx, o); err != nil {
		t.Fatalf("первый Save: %v", err)
	}
	if err := st.Save(ctx, o); err == nil {
		t.Fatal("повторный Save с тем же ID прошёл — первичный ключ не работает")
	}
}

type nopNotifier struct{}

func (nopNotifier) Notify(context.Context, string, string) error { return nil }
