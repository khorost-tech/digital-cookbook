// listen-notify проверяет LISTEN/NOTIFY через transaction pooling и напрямую.
// LISTEN требует ПОСТОЯННОГО server-соединения: подписка живёт, пока держится тот же
// backend. В transaction pooling server отвязывается после транзакции, поэтому
// уведомления теряются. Слушатель и отправитель — разные соединения.
//
//	go run . [target_dsn]   (по умолчанию проверяет и pgbouncer, и postgres)
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

// подписаться на канал, дождаться уведомления (или таймаут), вернуть true если пришло
func listenGot(dsn string) (bool, error) {
	ctx := context.Background()
	listener, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return false, fmt.Errorf("listener connect: %w", err)
	}
	defer listener.Close(ctx)
	if _, err := listener.Exec(ctx, "LISTEN demo_chan"); err != nil {
		return false, fmt.Errorf("LISTEN: %w", err)
	}

	// отдельное соединение шлёт NOTIFY (как другой клиент/процесс)
	notifier, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return false, fmt.Errorf("notifier connect: %w", err)
	}
	defer notifier.Close(ctx)
	// небольшая пауза, чтобы LISTEN точно зарегистрировался
	time.Sleep(200 * time.Millisecond)
	if _, err := notifier.Exec(ctx, "NOTIFY demo_chan, 'hello'"); err != nil {
		return false, fmt.Errorf("NOTIFY: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := listener.WaitForNotification(waitCtx); err != nil {
		return false, nil // таймаут = уведомление не дошло
	}
	return true, nil
}

func trial(name, dsn string) {
	got, err := listenGot(dsn)
	switch {
	case err != nil:
		fmt.Printf("%-30s ошибка: %v\n", name, err)
	case got:
		fmt.Printf("%-30s уведомление ПОЛУЧЕНО\n", name)
	default:
		fmt.Printf("%-30s уведомление ПОТЕРЯНО (за 2 c не пришло)\n", name)
	}
}

func main() {
	if len(os.Args) > 1 {
		trial("target", os.Args[1])
		return
	}
	trial("через PgBouncer (transaction)", "postgres://postgres:pooldemo@localhost:6432/opsdemo")
	trial("напрямую к postgres", "postgres://postgres:pooldemo@localhost:5436/opsdemo")
}
