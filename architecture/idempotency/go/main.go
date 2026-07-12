// Стенд к статье «Гарантии доставки и идемпотентность» на khorost.tech.
// https://khorost.tech/architecture/delivery-guarantees-idempotency/
//
// Что демонстрирует: связку «at-least-once доставка + идемпотентный консьюмер =
// effectively-once». Источник эмитит события списания кислорода из бака со
// стабильным event_id. «Брокер» имитирует at-least-once — доставляет часть
// событий ДВАЖДЫ. Один и тот же поток доставок обрабатывают три консьюмера:
//
//	(a) наивный дельта     — balance -= amount на каждую доставку.
//	                         Дубли ЗАДВАИВАЮТ списание → баланс НЕВЕРНЫЙ (ожидаемо, ломаем нарочно).
//	(b) dedup-ключ         — в ОДНОЙ транзакции: пытаемся записать event_id в таблицу
//	                         обработанных ключей; если ключ новый — применяем эффект,
//	                         если уже был — пропускаем. Дубли безвредны → баланс ВЕРНЫЙ.
//	(c) естественная       — событие несёт итоговый снимок remaining; применение
//	    идемпотентность      balance = remaining (UPSERT) идемпотентно само по себе,
//	                         дубли безвредны БЕЗ хранилища ключей → баланс ВЕРНЫЙ.
//
// Ассерты падают (log.Fatalf) при расхождении: у (b) и (c) итог == сумме уникальных
// списаний; у (a) итог обязан быть НЕВЕРНЫМ (иначе задвоение не воспроизвелось).
//
// Запуск:
//
//	cd idempotency/compose && docker compose up -d
//	cd ../go && go run .
//
// Строку подключения можно переопределить через DATABASE_URL (по умолчанию
// localhost:5450 — хостовый порт стенда, см. compose/compose.yml).
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// tank — единственный бак в демонстрации.
const tank = "T-1"

// startBalance — стартовый объём кислорода в баке (условные единицы).
const startBalance int64 = 1000

// event — событие списания кислорода.
//
//	eventID   — стабильный идентификатор события (одинаков у оригинала и его дубля).
//	amount    — сколько списать в этом событии.
//	remaining — итоговый снимок остатка ПОСЛЕ этого списания (для варианта (c)).
//	            Это «естественная идемпотентность»: событие несёт не дельту, а
//	            конечное значение, поэтому повторное применение ничего не меняет.
type event struct {
	eventID   string
	amount    int64
	remaining int64
}

func main() {
	ctx := context.Background()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://idem:idem@localhost:5450/idem"
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("подключение к Postgres: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("Postgres недоступен по %s: %v (поднят ли стенд? cd idempotency/compose && docker compose up -d)", dsn, err)
	}

	if err := setupSchema(ctx, pool); err != nil {
		log.Fatalf("инициализация схемы: %v", err)
	}

	// --- Источник: генерируем поток уникальных событий списания ---
	// Амплитуды подобраны так, чтобы сумма не ушла в минус. remaining считаем
	// как бегущий остаток — это и есть «снимок» для естественной идемпотентности.
	amounts := []int64{50, 30, 70, 20, 90, 40, 60, 25}
	events := make([]event, 0, len(amounts))
	running := startBalance
	var uniqueSpend int64
	for i, a := range amounts {
		running -= a
		uniqueSpend += a
		events = append(events, event{
			eventID:   fmt.Sprintf("evt-%03d", i+1),
			amount:    a,
			remaining: running,
		})
	}
	expected := startBalance - uniqueSpend // корректный итог после уникальных списаний

	// --- «Брокер» с семантикой at-least-once ---
	// Каждое третье событие доставляется ДВАЖДЫ (инъекция дубля сразу за оригиналом,
	// порядок сохраняется — так дубль варианта (c) безопасен и без хранилища ключей).
	deliveries := make([]event, 0, len(events))
	dupCount := 0
	for i, e := range events {
		deliveries = append(deliveries, e)
		if (i+1)%3 == 0 {
			deliveries = append(deliveries, e) // повторная доставка того же event_id
			dupCount++
		}
	}

	fmt.Printf("Бак %s: старт %d, уникальных событий %d, доставок (с дублями) %d, инъецировано дублей %d\n",
		tank, startBalance, len(events), len(deliveries), dupCount)
	fmt.Printf("Сумма уникальных списаний %d → корректный итог должен быть %d\n\n", uniqueSpend, expected)

	// --- Прогоняем ОДИН И ТОТ ЖЕ поток доставок через три консьюмера ---
	naive, err := runNaive(ctx, pool, deliveries)
	if err != nil {
		log.Fatalf("вариант (a): %v", err)
	}
	dedup, dropped, err := runDedup(ctx, pool, deliveries)
	if err != nil {
		log.Fatalf("вариант (b): %v", err)
	}
	natural, err := runNatural(ctx, pool, deliveries)
	if err != nil {
		log.Fatalf("вариант (c): %v", err)
	}

	// --- Итоги ---
	fmt.Println("Итоговые балансы:")
	fmt.Printf("  (a) наивный дельта          balance=%d  (ожидаемо НЕВЕРНО, задвоено на %d)\n", naive, expected-naive)
	fmt.Printf("  (b) dedup-ключ              balance=%d  (отброшено дублей: %d)\n", dedup, dropped)
	fmt.Printf("  (c) естественная идемпот.   balance=%d  (хранилище ключей не нужно)\n", natural)
	fmt.Println()

	// --- Ассерты (падают при расхождении с ожиданием) ---
	if dedup != expected {
		log.Fatalf("АССЕРТ (b) провален: dedup баланс %d != ожидаемого %d", dedup, expected)
	}
	if natural != expected {
		log.Fatalf("АССЕРТ (c) провален: естественная идемпотентность %d != ожидаемого %d", natural, expected)
	}
	if dropped != dupCount {
		log.Fatalf("АССЕРТ (b) провален: отброшено дублей %d, а инъецировано %d", dropped, dupCount)
	}
	// Вариант (a) ОБЯЗАН быть неверным — иначе задвоение не воспроизвелось и демонстрация бессмысленна.
	if naive == expected {
		log.Fatalf("АССЕРТ (a) провален: наивный баланс %d совпал с корректным — дубли не задвоили списание", naive)
	}

	fmt.Println("OK: (b) dedup-ключ и (c) естественная идемпотентность дают effectively-once")
	fmt.Println("    поверх at-least-once; (a) наивный дельта задваивает списание на дублях.")
}

// setupSchema создаёт таблицы (idempotent) и сбрасывает данные, чтобы повторные
// запуски `go run .` были воспроизводимы. Балансы каждого варианта — в отдельной
// строке таблицы balances (variant = a/b/c). Обработанные ключи — только для (b).
func setupSchema(ctx context.Context, pool *pgxpool.Pool) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS balances (
    variant text   NOT NULL,
    tank    text   NOT NULL,
    balance bigint NOT NULL,
    PRIMARY KEY (variant, tank)
);
CREATE TABLE IF NOT EXISTS processed_keys (
    event_id     text        PRIMARY KEY,
    processed_at timestamptz NOT NULL DEFAULT now()
);`
	if _, err := pool.Exec(ctx, ddl); err != nil {
		return err
	}
	// Сброс состояния к старту.
	if _, err := pool.Exec(ctx, `TRUNCATE processed_keys`); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM balances WHERE tank=$1`, tank); err != nil {
		return err
	}
	for _, v := range []string{"a", "b", "c"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO balances(variant, tank, balance) VALUES($1,$2,$3)`,
			v, tank, startBalance); err != nil {
			return err
		}
	}
	return nil
}

// runNaive — вариант (a): на КАЖДУЮ доставку безусловно вычитаем amount.
// Никакой защиты от дублей — повторная доставка списывает ещё раз. Итог занижен.
func runNaive(ctx context.Context, pool *pgxpool.Pool, deliveries []event) (int64, error) {
	for _, e := range deliveries {
		if _, err := pool.Exec(ctx,
			`UPDATE balances SET balance = balance - $1 WHERE variant='a' AND tank=$2`,
			e.amount, tank); err != nil {
			return 0, err
		}
	}
	return readBalance(ctx, pool, "a")
}

// runDedup — вариант (b): дедупликация по event_id в ОДНОЙ транзакции.
// INSERT ... ON CONFLICT DO NOTHING атомарно решает «ключ новый или уже был»:
// если строка вставлена (RowsAffected==1) — применяем эффект в той же транзакции;
// если конфликт (дубль) — эффект не применяем. Запись ключа и списание
// коммитятся вместе, поэтому падение между ними не разъедет состояние.
// Возвращает итоговый баланс и число отброшенных дублей.
func runDedup(ctx context.Context, pool *pgxpool.Pool, deliveries []event) (int64, int, error) {
	dropped := 0
	for _, e := range deliveries {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return 0, 0, err
		}
		tag, err := tx.Exec(ctx,
			`INSERT INTO processed_keys(event_id) VALUES($1) ON CONFLICT (event_id) DO NOTHING`,
			e.eventID)
		if err != nil {
			_ = tx.Rollback(ctx)
			return 0, 0, err
		}
		if tag.RowsAffected() == 1 {
			// Ключ новый — применяем эффект в той же транзакции.
			if _, err := tx.Exec(ctx,
				`UPDATE balances SET balance = balance - $1 WHERE variant='b' AND tank=$2`,
				e.amount, tank); err != nil {
				_ = tx.Rollback(ctx)
				return 0, 0, err
			}
		} else {
			// Ключ уже обработан — это дубль, пропускаем эффект.
			dropped++
		}
		if err := tx.Commit(ctx); err != nil {
			return 0, 0, err
		}
	}
	bal, err := readBalance(ctx, pool, "b")
	return bal, dropped, err
}

// runNatural — вариант (c): естественная идемпотентность. Событие несёт итоговый
// снимок remaining, поэтому применение balance = remaining (UPSERT) не зависит от
// того, сколько раз доставлено это событие: повторное применение того же снимка —
// no-op по значению. Хранилище обработанных ключей НЕ требуется.
//
// Важно: при at-least-once дубли приходят в порядке (сразу за оригиналом), поэтому
// снимок безопасен. Если бы доставка ещё и переупорядочивала события, для «последний
// побеждает» понадобился бы монотонный номер версии в WHERE (здесь не нужен).
func runNatural(ctx context.Context, pool *pgxpool.Pool, deliveries []event) (int64, error) {
	for _, e := range deliveries {
		if _, err := pool.Exec(ctx,
			`INSERT INTO balances(variant, tank, balance) VALUES('c', $1, $2)
			 ON CONFLICT (variant, tank) DO UPDATE SET balance = EXCLUDED.balance`,
			tank, e.remaining); err != nil {
			return 0, err
		}
	}
	return readBalance(ctx, pool, "c")
}

func readBalance(ctx context.Context, pool *pgxpool.Pool, variant string) (int64, error) {
	var bal int64
	err := pool.QueryRow(ctx,
		`SELECT balance FROM balances WHERE variant=$1 AND tank=$2`, variant, tank).Scan(&bal)
	if err == pgx.ErrNoRows {
		return 0, fmt.Errorf("нет строки баланса для variant=%s", variant)
	}
	return bal, err
}
