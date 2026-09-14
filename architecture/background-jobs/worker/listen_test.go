package main

import (
	"context"
	"testing"
	"time"
)

// Без уведомления WaitForJob не виснет навсегда, а отпускает по таймауту и
// без ошибки — воркер обязан вернуться в основной цикл и проверить дедлайн.
// Падающий вариант: если бы функция трактовала истечение waitCtx как ошибку
// (например, всегда возвращала err вместо nil при DeadlineExceeded), тест
// упал бы на проверке err != nil. Проверено вручную: временно заменил
// `return false, nil` на `return false, waitCtx.Err()` в listen.go — тест
// действительно падает с "WaitForJob вернул ошибку: context deadline exceeded".
func TestWaitForJobTimesOutWithoutNotification(t *testing.T) {
	db := setup(t) // гарантирует доступный Postgres тем же способом, что и остальные тесты пакета
	_ = db

	start := time.Now()
	ok, err := WaitForJob(context.Background(), testDSN(), 300*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("WaitForJob вернул ошибку: %v", err)
	}
	if ok {
		t.Fatal("WaitForJob вернул true без единого NOTIFY")
	}
	if elapsed < 300*time.Millisecond {
		t.Fatalf("вернулся раньше таймаута: %v", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("завис заметно дольше таймаута: %v", elapsed)
	}
}

// NOTIFY в jobs_new будит WaitForJob раньше таймаута. Падающий вариант: если
// бы LISTEN уходил через пул (общее соединение подменялось бы между вызовами)
// или если бы имя канала было перепутано, ожидание досиживало бы весь
// таймаут — тест ловит это по времени: пробуждение должно уложиться в малую
// долю от таймаута, а не в него целиком.
func TestWaitForJobWakesOnNotify(t *testing.T) {
	db := setup(t)

	done := make(chan struct {
		ok  bool
		err error
	}, 1)
	start := time.Now()
	go func() {
		ok, err := WaitForJob(context.Background(), testDSN(), 5*time.Second)
		done <- struct {
			ok  bool
			err error
		}{ok, err}
	}()

	// Даём LISTEN время встать на соединении до NOTIFY: если бы мы уведомили
	// раньше, чем горутина выполнила "LISTEN jobs_new", уведомление ушло бы в
	// никуда (Postgres не буферизует NOTIFY для ещё не подписавшихся).
	time.Sleep(200 * time.Millisecond)
	if _, err := db.Exec(context.Background(),
		"INSERT INTO jobs (kind) VALUES ('notify-test')"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	select {
	case res := <-done:
		elapsed := time.Since(start)
		if res.err != nil {
			t.Fatalf("WaitForJob вернул ошибку: %v", res.err)
		}
		if !res.ok {
			t.Fatal("WaitForJob вернул false несмотря на NOTIFY")
		}
		if elapsed > 2*time.Second {
			t.Fatalf("проснулся слишком поздно для NOTIFY (%v) — похоже на деградацию к таймауту", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForJob не вернул управление за 5с — NOTIFY не дошёл")
	}
}
