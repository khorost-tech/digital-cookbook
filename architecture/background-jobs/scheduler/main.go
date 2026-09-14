// Планировщик: N реплик, но тик выполняется ровно один раз. Координация — через
// pg_try_advisory_xact_lock: механика распределённых локов (аренда, fencing, кворум)
// разбирается в architecture/distributed-locking-coordination, здесь лок —
// рабочий инструмент, а не предмет изучения.
//
// Выбор ИМЕННО транзакционного варианта (_xact_lock, не «сессионный»
// pg_try_advisory_lock) — не стилистика, а способ обойти классическую ловушку с
// пулом соединений: сессионный лок живёт до pg_advisory_unlock ИЛИ до закрытия
// соединения, и при работе через pgxpool лок мог бы уйти на одно соединение из
// пула, а разблокировка — на другое. Лок на транзакцию снимается сам при
// COMMIT/ROLLBACK этой же транзакции, поэтому вопрос «какое именно соединение его
// держит» не встаёт вовсе — держит его конкретная транзакция, а не абстрактная
// сессия из пула.
//
//	go run . -id s1 -every 500ms -for 5s                # рабочий вариант, лок включён
//	go run . -id s1 -every 500ms -for 5s -lock=false     # падающий вариант, без координации
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const lockKey = 4242

func main() { os.Exit(run()) }

func run() int {
	dsn := flag.String("dsn", "postgres://jobs:jobs@localhost:5456/jobs", "PostgreSQL DSN")
	id := flag.String("id", "s1", "идентификатор реплики")
	every := flag.Duration("every", 500*time.Millisecond, "период тика")
	dur := flag.Duration("for", 5*time.Second, "сколько работать")
	lock := flag.Bool("lock", true, "брать advisory-лок вокруг проверки-и-вставки (false — падающий вариант)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *dur)
	defer cancel()

	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		fmt.Println("pgxpool:", err)
		return 1
	}
	defer pool.Close()

	// Схема ОДНА И ТА ЖЕ для обоих вариантов, и в ней СОЗНАТЕЛЬНО НЕТ UNIQUE(slot).
	// Причина — суть демонстрации: UNIQUE(slot) сам по себе гарантирует «одна
	// строка на слот» СРЕДСТВАМИ БД, независимо от того, берёт приложение лок или
	// нет. С таким ограничением прогон без лока дал бы тот же результат, что и с
	// локом, и демонстрация доказывала бы работу ограничения, а не координации.
	// Урок сам по себе полезный: если «ровно один раз» нужно именно как запись в
	// таблице, UNIQUE — правильное и более дешёвое решение, чем лок. Но здесь
	// проверяется другое — что эксклюзивность даёт лок, — поэтому подпорку в виде
	// ограничения убираем и оставляем механизм без страховки.
	if err := ensureTickLog(ctx, pool); err != nil {
		fmt.Println("create tick_log:", err)
		return 1
	}

	ticker := time.NewTicker(*every)
	defer ticker.Stop()
	won := 0
	for {
		select {
		case <-ctx.Done():
			fmt.Printf("реплика %s: записала тиков=%d (lock=%v)\n", *id, won, *lock)
			return 0
		case t := <-ticker.C:
			// slot — общий для всех реплик номер интервала: без него каждая
			// реплика считала бы «свой» тик отдельным событием. Ширина слота равна
			// периоду тика, поэтому один тик приходится ровно на один слот (иначе
			// одна реплика могла бы законно потикать дважды в один слот, и разница
			// между вариантами смазалась бы).
			slot := t.UnixMilli() / every.Milliseconds()
			ok, err := tryTick(ctx, pool, slot, *id, *lock)
			if err != nil {
				fmt.Println("tick:", err)
				return 1
			}
			if ok {
				won++
			}
		}
	}
}

// tryTick выполняет тик слота slot по схеме «проверить и вставить»: посмотреть,
// не записан ли слот кем-то другим, и записать, если нет. Это обычная форма
// запланированной работы — «сделай, если ещё не сделано за этот интервал».
//
// Ветвление по useLock касается РОВНО ОДНОГО: берётся ли advisory-лок вокруг этой
// пары операций. Сам запрос, схема таблицы, период и всё остальное — одинаковы.
//
//   - useLock=true: пара «проверка + вставка» выполняется под
//     pg_try_advisory_xact_lock, то есть становится атомарной по отношению к другим
//     репликам. Реплика, не получившая лок, сразу возвращает false и ничего не
//     пишет: это не её тик. Результат — одна строка на слот.
//   - useLock=false: та же пара выполняется без координации. При read committed
//     все реплики успевают прочитать «слота ещё нет» ДО того, как любая из них
//     закоммитит вставку, и вставляют все — классическая гонка проверки и
//     действия. Результат — несколько строк на один слот.
//
// Именно поэтому в таблице нет UNIQUE(slot): ограничение схлопнуло бы результат к
// одной строке на слот в ОБОИХ вариантах и скрыло бы разницу (см. комментарий к
// схеме выше).
func tryTick(ctx context.Context, pool *pgxpool.Pool, slot int64, runner string, useLock bool) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background())

	if useLock {
		var got bool
		if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", int64(lockKey)).Scan(&got); err != nil {
			return false, err
		}
		if !got {
			return false, nil // лок у другой реплики — этот тик не наш
		}
	}

	// Проверка и вставка одним запросом. Атомарной по отношению к другим репликам
	// её делает НЕ этот SQL (WHERE NOT EXISTS проверяет снимок своей транзакции и
	// от параллельной вставки не защищает), а advisory-лок выше — когда он взят.
	tag, err := tx.Exec(ctx, `
		INSERT INTO tick_log (slot, runner)
		SELECT $1, $2
		WHERE NOT EXISTS (SELECT 1 FROM tick_log WHERE slot = $1)`, slot, runner)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ensureTickLog создаёт таблицу, переживая ОДНОВРЕМЕННЫЙ старт нескольких реплик.
//
// `CREATE TABLE IF NOT EXISTS` от гонки НЕ защищает: проверка существования и
// вставка в системный каталог не атомарны между собой, и при параллельном старте
// на пустой БД часть реплик падает с unique_violation по pg_class/pg_type. В
// демонстрации через scheduler-demo.sh этого не видно — там таблица создаётся до
// запуска реплик, — но запуск нескольких реплик руками (он описан в шапке файла)
// ронял две из трёх, и координация «трёх реплик» молча вырождалась в одну.
// Ошибка каталога здесь означает ровно одно: таблицу создал кто-то другой в тот
// же момент, то есть цель достигнута. Поэтому она поглощается ОДИН раз, после
// чего результат перепроверяется — если таблицы всё же нет, ошибка настоящая.
func ensureTickLog(ctx context.Context, pool *pgxpool.Pool) error {
	const ddl = `
		CREATE TABLE IF NOT EXISTS tick_log (
			id       BIGSERIAL PRIMARY KEY,
			slot     BIGINT      NOT NULL,
			runner   TEXT        NOT NULL,
			at       TIMESTAMPTZ NOT NULL DEFAULT now()
		)`

	_, err := pool.Exec(ctx, ddl)
	if err == nil {
		return nil
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgerrcode.UniqueViolation {
		return err // ошибка не про гонку в каталоге — отдаём как есть
	}

	// Проигравший гонку убеждается, что таблица действительно появилась.
	var exists bool
	if err := pool.QueryRow(ctx,
		"SELECT to_regclass('tick_log') IS NOT NULL").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("конфликт при создании tick_log, но таблицы нет: %w", pgErr)
	}
	return nil
}
