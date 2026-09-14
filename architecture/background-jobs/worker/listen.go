package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// WaitForJob блокируется до уведомления в канале jobs_new или до таймаута.
// Отдельное соединение (не пул): LISTEN живёт на конкретном соединении, а пул
// волен отдать другое — уведомление тогда просто не придёт. pgxpool отдаёт
// разные физические соединения на разные запросы (и возвращает их в пул между
// вызовами), так что LISTEN через пул слушал бы одно соединение, а следующий
// запрос воркера ушёл бы на другое — уведомление осело бы там, где его никто
// не ждёт. Поэтому здесь pgx.Connect напрямую, соединение открывается заново
// на каждый вызов и закрывается в конце: воркер и так спит здесь секундами,
// цена переподключения на этом фоне не важна.
func WaitForJob(ctx context.Context, dsn string, timeout time.Duration) (bool, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return false, err
	}
	defer conn.Close(context.Background())

	if _, err := conn.Exec(ctx, "LISTEN jobs_new"); err != nil {
		return false, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if _, err := conn.WaitForNotification(waitCtx); err != nil {
		if waitCtx.Err() != nil {
			return false, nil // таймаут — уведомления не было
		}
		return false, err
	}
	return true, nil
}
