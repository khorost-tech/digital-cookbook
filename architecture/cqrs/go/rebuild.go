package main

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// rebuildReadModel — blue-green пересборка проекции без остановки чтения.
//
// Идея: старая проекция orders_read продолжает обслуживать читателей. Рядом с нуля
// строим orders_read_v2 полным реплеем лога (например, изменили схему проекции или
// чинят баг проектора). Когда v2 догнала хвост — атомарно меняем таблицы местами
// одной DDL-транзакцией. Postgres выполняет RENAME транзакционно: читатели до COMMIT
// видят старую orders_read, после — новую, без промежуточного «нет таблицы».
//
// Возвращает число событий, реплейнутых в v2.
func rebuildReadModel(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	// 1) Строим v2 «зелёную» рядом с «синей» orders_read. Схема идентична — в реальной
	//    пересборке здесь могла бы быть новая/исправленная структура проекции.
	_, err := pool.Exec(ctx, `
		DROP TABLE IF EXISTS orders_read_v2;
		CREATE TABLE orders_read_v2 (
			order_id    BIGINT PRIMARY KEY,
			user_id     TEXT   NOT NULL,
			status      TEXT   NOT NULL,
			amount      BIGINT NOT NULL,
			updated_seq BIGINT NOT NULL
		);
		CREATE INDEX ON orders_read_v2 (user_id);
		INSERT INTO projection_checkpoint (name, last_seq) VALUES ('orders_read_v2', 0)
			ON CONFLICT (name) DO UPDATE SET last_seq = 0;
	`)
	if err != nil {
		return 0, err
	}

	// 2) Реплеим весь лог в v2 (той же идемпотентной механикой applyLog). Чтение
	//    orders_read в это время не блокируется — работаем в отдельной таблице.
	applied, err := applyLog(ctx, pool, "orders_read_v2", "projection_checkpoint")
	if err != nil {
		return 0, err
	}

	// 3) Атомарный blue-green switch: меняем таблицы местами в одной транзакции.
	//    Заодно переносим позицию чекпоинта, чтобы штатный проектор продолжил с того
	//    места, до которого догнала v2.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// Многооператорный DDL без параметров (simple-протокол pgx) — все переименования
	// и перенос позиции чекпоинта одной транзакцией. Параметров тут нет намеренно:
	// значение v2-чекпоинта берём подзапросом прямо в SQL.
	batch := `
		DROP TABLE IF EXISTS orders_read_old;
		ALTER TABLE orders_read    RENAME TO orders_read_old;
		ALTER TABLE orders_read_v2 RENAME TO orders_read;
		UPDATE projection_checkpoint SET last_seq =
			(SELECT last_seq FROM projection_checkpoint WHERE name='orders_read_v2')
			WHERE name='orders_read';
		DELETE FROM projection_checkpoint WHERE name='orders_read_v2';
		DROP TABLE orders_read_old;
	`
	if _, err = tx.Exec(ctx, batch); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return applied, nil
}
