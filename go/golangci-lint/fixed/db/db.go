// Package db — исправленная пара sample/db.
package db

import (
	"context"
	"database/sql"
)

// Names проверяет rows.Err() после цикла.
func Names(ctx context.Context, d *sql.DB) ([]string, error) {
	rows, err := d.QueryContext(ctx, "SELECT name FROM users")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// First закрывает rows и при раннем выходе.
func First(ctx context.Context, d *sql.DB) (string, error) {
	rows, err := d.QueryContext(ctx, "SELECT name FROM users")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return "", err
		}
		return n, nil
	}
	return "", rows.Err()
}
