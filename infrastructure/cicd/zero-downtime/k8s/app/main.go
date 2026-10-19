// Приложение уровня B: несколько реплик в Deployment бьют по PostgreSQL в
// кластере. Версия схемы вшивается при сборке через ldflags (main.Schema) —
// ТРИ образа из одного файла:
//
//	old  -> тег v1:   пишет и читает amount_cents (старый код);
//	dual -> тег dual: пишет ОБЕ колонки, читает COALESCE(total, amount).
//	                  Промежуточная версия перехода: она делает откат безопасным
//	                  и не даёт readiness принять NULL за успех, пока backfill
//	                  ещё не прошёл;
//	new  -> тег v2:   пишет и читает total_cents (новый код).
//
// Ключевой сигнал демонстрации — READINESS. /healthz обращается к СВОЕЙ колонке;
// если несовместимая миграция её переименовала, проба падает (503), под уходит
// в NotReady и Service перестаёт слать на него трафик. Это и есть наблюдаемое
// проявление того, что во время rolling update старый код видит несовместимую
// схему. Фоновый writer добавляет реальную нагрузку и считает ошибки в /stats.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Schema задаётся при сборке: -ldflags "-X main.Schema=old" | "-X main.Schema=new".
var Schema = "old"

var column string

var okCount, errCount int64

// insertSQL — как версия ПИШЕТ, column — какую колонку она ЧИТАЕТ (в том числе
// в readiness-пробе).
var insertSQL string

func main() {
	switch Schema {
	case "old":
		// Старый код: знает только исходную колонку.
		column = "amount_cents"
		insertSQL = "INSERT INTO orders (customer, amount_cents) VALUES ($1, $2)"
	case "dual":
		// Промежуточная версия перехода: пишет ОБЕ колонки, читает переходным
		// COALESCE. Читать просто total_cents здесь НЕЛЬЗЯ: пока идёт выкат,
		// старые поды продолжают создавать строки только с amount_cents, и у
		// них total_cents пуст. Версия, читающая одну лишь новую колонку,
		// увидела бы там NULL — то есть отсутствие данных, замаскированное под
		// корректный ответ. COALESCE делает переходное чтение честным: берём
		// новое значение, а если его ещё нет — старое.
		column = "COALESCE(total_cents, amount_cents)"
		insertSQL = "INSERT INTO orders (customer, amount_cents, total_cents) VALUES ($1, $2, $2)"
	case "new":
		// Новый код: только новая колонка. Выкатывается ПОСЛЕ того, как
		// промежуточная версия заполнила обе.
		column = "total_cents"
		insertSQL = "INSERT INTO orders (customer, total_cents) VALUES ($1, $2)"
	default:
		fmt.Fprintf(os.Stderr, "неизвестная Schema %q (old|dual|new)\n", Schema)
		os.Exit(2)
	}

	dsn := os.Getenv("DSN")
	if dsn == "" {
		dsn = "postgres://zdt:zdt@postgres:5432/zdt"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "подключение: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Фоновая нагрузка: пишем и читаем через свою колонку.
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for range t.C {
			_, e := pool.Exec(ctx, insertSQL, "k8s", 42)
			if e != nil {
				atomic.AddInt64(&errCount, 1)
				fmt.Fprintf(os.Stderr, "ERROR schema=%s: %v\n", Schema, e)
			} else {
				atomic.AddInt64(&okCount, 1)
			}
		}
	}()

	// Readiness: версия готова, если может ПРОЧИТАТЬ БИЗНЕС-ЗНАЧЕНИЕ последней
	// строки. Проверять только доступность колонки мало: NULL в ней означает,
	// что данные этой версии не видны, — и если считать такой ответ успехом,
	// проба маскирует ровно ту поломку, которую должна ловить. Поэтому пустое
	// значение при существующей строке — это 503.
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var v *int
		e := pool.QueryRow(cctx,
			fmt.Sprintf("SELECT %s FROM orders ORDER BY id DESC LIMIT 1", column)).Scan(&v)
		if e != nil {
			// Пустая таблица — не повод считать под неготовым.
			if errors.Is(e, pgx.ErrNoRows) {
				fmt.Fprintf(w, "ok schema=%s (таблица пуста)\n", Schema)
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "unhealthy schema=%s: %v\n", Schema, e)
			return
		}
		if v == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "unhealthy schema=%s: читаемое значение (%s) пусто\n", Schema, column)
			return
		}
		fmt.Fprintf(w, "ok schema=%s value=%d\n", Schema, *v)
	})

	http.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema": Schema,
			"ok":     atomic.LoadInt64(&okCount),
			"err":    atomic.LoadInt64(&errCount),
		})
	})

	_ = http.ListenAndServe(":8080", nil)
}
