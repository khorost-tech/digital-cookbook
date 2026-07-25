package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

type mutationOutcome struct {
	submitDuration     time.Duration // время, за которое ALTER вернул управление клиенту
	completionDuration time.Duration // время до is_done=1 в system.mutations — реальная стоимость
	partsToDoAtSubmit  int64
}

// runMutation отправляет ALTER TABLE ... (UPDATE/DELETE) БЕЗ mutations_sync
// (реальное поведение по умолчанию — асинхронная мутация), затем поллит
// system.mutations по mutation_id до is_done=1 С ТАЙМАУТОМ (не бесконечно) —
// тот же паттерн, что ../when-olap/pointops.go chMutation, обобщённый на
// произвольную таблицу/WHERE (тут — по некластерному ключу country, не PK).
func runMutation(ctx context.Context, conn clickhouse.Conn, table, alterSQL string, timeout time.Duration) (mutationOutcome, error) {
	submitStart := time.Now()
	if err := conn.Exec(ctx, alterSQL); err != nil {
		return mutationOutcome{}, fmt.Errorf("alter: %w", err)
	}
	submitDur := time.Since(submitStart)

	var mutationID string
	var isDone uint8
	var partsToDo int64
	err := conn.QueryRow(ctx, `
		SELECT mutation_id, is_done, parts_to_do
		FROM system.mutations
		WHERE database = 'demo' AND table = ?
		ORDER BY create_time DESC
		LIMIT 1`, table).Scan(&mutationID, &isDone, &partsToDo)
	if err != nil {
		return mutationOutcome{}, fmt.Errorf("find mutation: %w", err)
	}
	out := mutationOutcome{submitDuration: submitDur, partsToDoAtSubmit: partsToDo}

	pollStart := time.Now()
	for isDone != 1 {
		if time.Since(pollStart) > timeout {
			return out, fmt.Errorf("mutation %s (table %s) did not finish within timeout %s", mutationID, table, timeout)
		}
		time.Sleep(200 * time.Millisecond)
		if err := conn.QueryRow(ctx, `
			SELECT is_done FROM system.mutations
			WHERE database = 'demo' AND table = ? AND mutation_id = ?`, table, mutationID).Scan(&isDone); err != nil {
			return out, fmt.Errorf("poll mutation: %w", err)
		}
	}
	out.completionDuration = time.Since(submitStart)
	return out, nil
}

// waitForRows поллит tableRows до совпадения с expected, с таймаутом (не
// бесконечно). Возвращает последнее прочитанное значение и ошибку, если
// expected не достигнут в пределах timeout.
func waitForRows(ctx context.Context, conn clickhouse.Conn, table string, expected uint64, timeout time.Duration) (uint64, error) {
	start := time.Now()
	var last uint64
	for {
		n, err := tableRows(ctx, conn, table)
		if err != nil {
			return 0, err
		}
		last = n
		if n == expected {
			return n, nil
		}
		if time.Since(start) > timeout {
			return last, fmt.Errorf("rows (%d) did not settle to expected (%d) within %s", last, expected, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// phaseMutations — Step 2 брифа: ALTER TABLE UPDATE/DELETE по некластерному
// ключу — показать асинхронность, переписывание parts (system.mutations
// is_done/parts_to_do), стоимость. Явный анти-паттерн: точечные апдейты в
// MergeTree переписывают ЦЕЛЫЕ parts, а не одну строку — WHERE country='KZ'/
// 'JP' в этом стенде затрагивает партии по всем месяцам датасета (90-дневное
// окно -> несколько toYYYYMM(event_time)-партиций), т.е. почти все parts.
func phaseMutations(ctx context.Context, ch clickhouse.Conn, table string, timeout time.Duration) {
	fmt.Println("\n=== MUTATIONS: UPDATE/DELETE — анти-паттерн точечных изменений (Step 2 брифа) ===")

	before, err := tableRows(ctx, ch, table)
	if err != nil {
		log.Fatalf("rows before mutations: %v", err)
	}

	var kzCount uint64
	if err := ch.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM demo.%s WHERE country = 'KZ'", table)).Scan(&kzCount); err != nil {
		log.Fatalf("count KZ: %v", err)
	}
	updSQL := fmt.Sprintf("ALTER TABLE demo.%s UPDATE revenue = 0 WHERE country = 'KZ'", table)
	upd, err := runMutation(ctx, ch, table, updSQL, timeout)
	if err != nil {
		log.Fatalf("update mutation: %v", err)
	}
	fmt.Printf("[mutations] ALTER ... UPDATE revenue=0 WHERE country='KZ' (%d строк подпадает): submit=%s (выглядит мгновенно), completion=%s (parts_to_do at submit=%d) — РЕАЛЬНАЯ стоимость\n",
		kzCount, upd.submitDuration, upd.completionDuration, upd.partsToDoAtSubmit)

	var jpCount uint64
	if err := ch.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM demo.%s WHERE country = 'JP'", table)).Scan(&jpCount); err != nil {
		log.Fatalf("count JP: %v", err)
	}
	delSQL := fmt.Sprintf("ALTER TABLE demo.%s DELETE WHERE country = 'JP'", table)
	del, err := runMutation(ctx, ch, table, delSQL, timeout)
	if err != nil {
		log.Fatalf("delete mutation: %v", err)
	}
	fmt.Printf("[mutations] ALTER ... DELETE WHERE country='JP' (%d строк удаляется): submit=%s (выглядит мгновенно), completion=%s (parts_to_do at submit=%d) — РЕАЛЬНАЯ стоимость\n",
		jpCount, del.submitDuration, del.completionDuration, del.partsToDoAtSubmit)

	// is_done=1 в system.mutations значит "команда мутации выполнена", но
	// живая находка: system.parts (source для tableRows) может на несколько
	// сотен мс отстать от этого флага — новый набор active parts после
	// DELETE не всегда виден мгновенно следующему запросу той же сессии.
	// Короткий поллинг с таймаутом делает финальную проверку честной, а не
	// гонкой на один SELECT.
	after, waitErr := waitForRows(ctx, ch, table, before-jpCount, 5*time.Second)
	if waitErr != nil {
		log.Fatalf("rows after mutations did not settle: %v", waitErr)
	}
	fmt.Printf("[mutations] rows: before=%d, after=%d (ожидаемо after == before - deleted(%d))\n", before, after, jpCount)

	fmt.Println("[mutations] почему это анти-паттерн: MergeTree parts иммутабельны — ALTER ... UPDATE/DELETE НЕ правит строку на месте, а перечитывает и переписывает целиком КАЖДЫЙ part, содержащий хоть одну подходящую под WHERE строку (parts_to_do выше — число таких parts на момент нашего первого чтения system.mutations после submit). При частых точечных изменениях (в отличие от OLTP UPDATE по PK) стоимость мутации растёт с размером затронутых partitions, а не с числом изменённых строк.")
	if del.partsToDoAtSubmit == 0 {
		fmt.Printf("[mutations] честная деталь: DELETE parts_to_do прочитан уже равным 0 — на маленьком поднаборе (несколько parts) мутация успела завершиться (is_done=1) быстрее, чем наш клиент сделал первый SELECT из system.mutations. Гонка честная: submit (%s) — это отправка ALTER, а не гарантия, что мутация ещё выполняется к моменту следующего запроса; сама completion-стоимость (%s) всё равно измерена корректно (submit -> is_done=1).\n", del.submitDuration, del.completionDuration)
	}

	assertFailFast(upd.completionDuration > 0, "UPDATE-мутация завершилась (is_done=1) в пределах таймаута %s: completion=%s", timeout, upd.completionDuration)
	assertFailFast(del.completionDuration > 0, "DELETE-мутация завершилась (is_done=1) в пределах таймаута %s: completion=%s", timeout, del.completionDuration)
	assertFailFast(upd.partsToDoAtSubmit >= 0 && del.partsToDoAtSubmit >= 0, "parts_to_do прочитан для обеих мутаций (UPDATE=%d, DELETE=%d)", upd.partsToDoAtSubmit, del.partsToDoAtSubmit)
	assertFailFast(after == before-jpCount, "после DELETE rows (%d) == before (%d) - deleted (%d), после settle-поллинга system.parts (до 5s)", after, before, jpCount)
}
