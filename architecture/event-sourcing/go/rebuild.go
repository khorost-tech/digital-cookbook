package main

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Rebuild собирает новую проекцию balances_v2 полным реплеем истории с global_pos=0,
// не останавливая онлайн-проектор balances. Blue-green: старая проекция продолжает
// обслуживать чтения, пока новая набивается в стороне.
func Rebuild(ctx context.Context, pool *pgxpool.Pool, batchSize int) (int, error) {
	// Начисто: чистим целевую таблицу и отдельный чекпоинт rebuild.
	if _, err := pool.Exec(ctx, `TRUNCATE balances_v2`); err != nil {
		return 0, err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM projection_checkpoint WHERE name='rebuild'`); err != nil {
		return 0, err
	}
	proj := NewProjector(pool, "balances_v2", "rebuild")
	return proj.Drain(ctx, batchSize)
}

// SwapProjections атомарно переключает balances <-> balances_v2 через RENAME.
// DDL в PostgreSQL транзакционен: читатели видят либо старую, либо новую таблицу,
// момента «без balances» не существует.
func SwapProjections(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	stmts := []string{
		`ALTER TABLE balances RENAME TO balances_tmp`,
		`ALTER TABLE balances_v2 RENAME TO balances`,
		`ALTER TABLE balances_tmp RENAME TO balances_v2`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(ctx, s); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
