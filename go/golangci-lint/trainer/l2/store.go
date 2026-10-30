// Package l2 — уровень 2: безопасность и ошибки, которые глазами пропускают.
package l2

import (
	"context"
	"database/sql"
	"math/rand"
	"strconv"
)

const dbPassword = "demo-not-a-real-secret-1234" // заведомо тестовое значение

// DSN собирает строку подключения.
func DSN(host string) string { return "postgres://app:" + dbPassword + "@" + host + "/app" }

// SessionID выдаёт идентификатор сессии.
func SessionID() string { return strconv.FormatInt(rand.Int63(), 36) }

// Emails читает адреса пользователей.
func Emails(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT email FROM users")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// Port разбирает порт; пустая строка — порт по умолчанию.
func Port(s string) (int, error) {
	var err error
	p := 8080
	if s != "" {
		p, err := strconv.Atoi(s)
		_, _ = p, err
	}
	return p, err
}
