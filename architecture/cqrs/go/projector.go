package main

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// setupReadModel создаёт read-сторону: денормализованную проекцию orders_read
// (заточена под запрос «заказы пользователя» — индекс по user_id) и таблицу чекпоинта
// projection_checkpoint (позиция, до которой проектор уже применил лог).
//
// Ключевой инвариант: чекпоинт двигается в ОДНОЙ транзакции с апдейтом проекции
// (см. projectOnce). Иначе при сбое между «применил» и «записал позицию» события
// либо потеряются, либо применятся дважды.
func setupReadModel(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		DROP TABLE IF EXISTS orders_read;
		CREATE TABLE orders_read (
			order_id    BIGINT PRIMARY KEY,
			user_id     TEXT   NOT NULL,
			status      TEXT   NOT NULL,
			amount      BIGINT NOT NULL,
			updated_seq BIGINT NOT NULL
		);
		CREATE INDEX ON orders_read (user_id);   -- проекция под конкретный запрос: список заказов юзера

		DROP TABLE IF EXISTS projection_checkpoint;
		CREATE TABLE projection_checkpoint (
			name     TEXT PRIMARY KEY,
			last_seq BIGINT NOT NULL
		);
		INSERT INTO projection_checkpoint (name, last_seq) VALUES ('orders_read', 0);
	`)
	return err
}

// projectOnce применяет к проекции все события лога с seq > чекпоинта и атомарно
// сдвигает чекпоинт. Применение идемпотентно (UPSERT по order_id), поэтому повтор
// того же батча после сбоя не ломает проекцию. Возвращает число применённых событий.
//
// Именно то, что этот шаг вызывается ОТДЕЛЬНО от команд записи, и создаёт лаг:
// пока projectOnce не позвали, свежие события уже в логе, но проекция их не видит.
func projectOnce(ctx context.Context, pool *pgxpool.Pool) (applied int, err error) {
	return applyLog(ctx, pool, "orders_read", "projection_checkpoint")
}

// applyLog — общая механика «применить хвост лога в таблицу-проекцию + сдвинуть
// чекпоинт в той же транзакции». Используется и штатным проектором, и rebuild-ом
// (там применяет в orders_read_v2 со своим чекпоинтом).
func applyLog(ctx context.Context, pool *pgxpool.Pool, table, ckpt string) (applied int, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var from int64
	if err = tx.QueryRow(ctx,
		`SELECT last_seq FROM `+ckpt+` WHERE name=$1 FOR UPDATE`, table).Scan(&from); err != nil {
		return 0, err
	}

	rows, err := tx.Query(ctx,
		`SELECT seq, order_id, user_id, type, amount FROM order_events
		  WHERE seq > $1 ORDER BY seq`, from)
	if err != nil {
		return 0, err
	}
	type ev struct {
		seq, orderID, amount int64
		userID, typ          string
	}
	var evs []ev
	for rows.Next() {
		var e ev
		if err = rows.Scan(&e.seq, &e.orderID, &e.userID, &e.typ, &e.amount); err != nil {
			rows.Close()
			return 0, err
		}
		evs = append(evs, e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}

	var last int64 = from
	for _, e := range evs {
		switch e.typ {
		case "created":
			// Идемпотентный UPSERT: повторное применение того же события безопасно.
			_, err = tx.Exec(ctx,
				`INSERT INTO `+table+` (order_id, user_id, status, amount, updated_seq)
				 VALUES ($1, $2, 'new', $3, $4)
				 ON CONFLICT (order_id) DO UPDATE
				   SET status=EXCLUDED.status, amount=EXCLUDED.amount, updated_seq=EXCLUDED.updated_seq`,
				e.orderID, e.userID, e.amount, e.seq)
		case "paid":
			_, err = tx.Exec(ctx,
				`UPDATE `+table+` SET status='paid', updated_seq=$2 WHERE order_id=$1`,
				e.orderID, e.seq)
		}
		if err != nil {
			return 0, err
		}
		last = e.seq
	}

	if _, err = tx.Exec(ctx,
		`UPDATE `+ckpt+` SET last_seq=$2 WHERE name=$1`, table, last); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(evs), nil
}

// checkpointSeq — до какого seq проектор уже применил лог.
func checkpointSeq(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var s int64
	err := pool.QueryRow(ctx,
		`SELECT last_seq FROM projection_checkpoint WHERE name='orders_read'`).Scan(&s)
	return s, err
}

// projectionLag — метрика отставания проекции: хвост лога минус чекпоинт проектора.
// 0 — проекция догнала лог; >0 — есть непроapplication-ные события (окно, в котором
// ломается read-your-writes).
func projectionLag(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tail, err := tailSeq(ctx, pool)
	if err != nil {
		return 0, err
	}
	ck, err := checkpointSeq(ctx, pool)
	if err != nil {
		return 0, err
	}
	return tail - ck, nil
}

// readUserOrdersFromProjection — read-путь через проекцию (быстро, денормализовано,
// но может отставать). Для «чужих» заказов этого достаточно; для собственных свежих —
// нет (см. read-your-writes в main.go).
func readUserOrdersFromProjection(ctx context.Context, pool *pgxpool.Pool, userID string) ([]Order, error) {
	rows, err := pool.Query(ctx,
		`SELECT order_id, user_id, amount, status, updated_seq
		   FROM orders_read WHERE user_id=$1 ORDER BY order_id`, userID)
	if err != nil {
		return nil, err
	}
	return scanOrders(rows)
}

// waitForProjection — второй приём read-your-writes: «подождать по токену». Крутит
// проектор, пока чекпоинт не дойдёт до нужного seq, после чего из проекции уже видно
// собственную запись. В проде это ожидание готовности проекции (а не busy-loop).
func waitForProjection(ctx context.Context, pool *pgxpool.Pool, token int64) error {
	for {
		ck, err := checkpointSeq(ctx, pool)
		if err != nil {
			return err
		}
		if ck >= token {
			return nil
		}
		if _, err := projectOnce(ctx, pool); err != nil {
			return err
		}
	}
}
