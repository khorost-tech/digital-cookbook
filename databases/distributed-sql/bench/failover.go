package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// failover — непрерывная запись в один ключ, пока скрипт гасит узел, где живёт
// лидер его диапазона. Окно недоступности с точки зрения приложения — самая
// длинная пауза между двумя успешными коммитами.
//
// Таймаут попытки нарочно длинный (по умолчанию 30 с): запрос, застрявший на
// погибшем лидере, должен дождаться нового лидера, а не оборваться раньше.
// Короткий таймаут подмешал бы в замер собственную величину.
func runFailover(db *sql.DB, engine string, key int64, dur, attemptTimeout time.Duration) {
	upd := "UPDATE kv SET v = v + 1 WHERE k = " + ph(engine, 1)
	sel := "SELECT v FROM kv WHERE k = " + ph(engine, 1)

	// Проверка сохранности: каждый успешный UPDATE прибавляет 1. Значит, прирост
	// за прогон обязан быть не меньше числа подтверждённых коммитов (иначе
	// подтверждённая запись потеряна) и не больше их числа плюс число попыток с
	// неизвестным исходом (таймаут, обрыв, 40003 — такая запись могла пройти).
	readV := func() int64 {
		var v int64
		for i := 0; i < 30; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := db.QueryRowContext(ctx, sel, key).Scan(&v)
			cancel()
			if err == nil {
				return v
			}
			time.Sleep(time.Second)
		}
		die("не удалось прочитать ключ %d для проверки сохранности", key)
		return 0
	}
	v0 := readV()
	acked, unknown := 0, 0
	start := time.Now()
	type sec struct{ ok, fail int }
	perSec := make([]sec, int(dur/time.Second)+1)
	lastOK := start
	var maxGap time.Duration
	var gapFrom time.Duration
	errs := map[string]int{}

	for time.Since(start) < dur {
		ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout)
		_, err := db.ExecContext(ctx, upd, key)
		cancel()
		now := time.Now()
		// Результат учитывается ВСЕГДА, даже если запрос завершился за пределами
		// окна: иначе его изменение попадёт в итоговое v, а подтверждение — нет.
		s := int(now.Sub(start) / time.Second)
		if s >= len(perSec) {
			s = len(perSec) - 1
		}
		if err != nil {
			unknown++
			perSec[s].fail++
			errs[errLine(err)]++
			// Не долбим мёртвый узел в цикле без паузы.
			time.Sleep(50 * time.Millisecond)
			continue
		}
		acked++
		perSec[s].ok++
		if g := now.Sub(lastOK); g > maxGap {
			maxGap, gapFrom = g, lastOK.Sub(start)
		}
		lastOK = now
	}
	fmt.Printf("engine=%s  ключ=%d  длительность=%s  таймаут попытки=%s\n", engine, key, dur, attemptTimeout)
	for i, s := range perSec {
		if s.ok+s.fail == 0 {
			continue
		}
		fmt.Printf("t=%2ds  ok=%-5d err=%d\n", i, s.ok, s.fail)
	}
	fmt.Printf("самая длинная пауза между коммитами: %.2f с (началась на %.1f с)\n",
		maxGap.Seconds(), gapFrom.Seconds())
	for e, n := range errs {
		fmt.Printf("ошибка x%d: %s\n", n, e)
	}
	v1 := readV()
	delta := v1 - v0
	verdict := "сходится: подтверждённые записи на месте"
	switch {
	case delta < int64(acked):
		verdict = "ПОТЕРЯ: прирост меньше числа подтверждённых коммитов"
	case delta > int64(acked+unknown):
		verdict = "ЛИШНЕЕ: прирост больше, чем подтверждённые плюс неизвестные"
	}
	fmt.Printf("сохранность: подтверждено %d, исход неизвестен %d, прирост v %d — %s\n",
		acked, unknown, delta, verdict)
}
