// Package db — чтение строк без проверки ошибки итерации и без закрытия.
package db

import (
	"context"
	"database/sql"
)

// Names не проверяет rows.Err(): обрыв соединения посреди выборки выглядит как конец данных.
func Names(ctx context.Context, d *sql.DB) ([]string, error) {
	rows, err := d.QueryContext(ctx, "SELECT name FROM users") // want rowserrcheck
	if err != nil {
		return nil, err
	}
	defer rows.Close() // noise errcheck
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// First берёт первую строку и выходит, не дочитав выборку и не закрыв rows:
// соединение остаётся занятым, пока rows не закрыт. Полный проход rows.Next()
// закрыл бы его сам — утечка именно в раннем выходе.
func First(ctx context.Context, d *sql.DB) (string, error) {
	rows, err := d.QueryContext(ctx, "SELECT name FROM users") // want sqlclosecheck
	if err != nil {
		return "", err
	}
	if rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return "", err
		}
		return n, nil
	}
	return "", rows.Err()
}
