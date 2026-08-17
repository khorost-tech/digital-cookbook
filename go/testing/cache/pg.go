package cache

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgRepo — адаптер ProfileRepository поверх pgx-пула соединений с PostgreSQL.
type pgRepo struct {
	pool *pgxpool.Pool
}

// NewPGRepo создаёт ProfileRepository, читающий профили из таблицы
// profiles(id, name, email) через pgxpool.Pool.
func NewPGRepo(pool *pgxpool.Pool) ProfileRepository {
	return &pgRepo{pool: pool}
}

// GetByID выполняет SELECT id,name,email FROM profiles WHERE id=$1.
// Если строк не найдено — возвращает ErrNotFound (не pgx.ErrNoRows,
// чтобы вызывающий код оставался независим от драйвера БД).
func (r *pgRepo) GetByID(ctx context.Context, id int64) (Profile, error) {
	var p Profile
	err := r.pool.QueryRow(ctx, `SELECT id, name, email FROM profiles WHERE id=$1`, id).
		Scan(&p.ID, &p.Name, &p.Email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Profile{}, ErrNotFound
		}
		return Profile{}, err
	}
	return p, nil
}
