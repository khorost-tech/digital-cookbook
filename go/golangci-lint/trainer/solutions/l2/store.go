// Package l2 — эталон уровня 2.
package l2

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strconv"
)

// DSN собирает строку подключения; пароль — из окружения.
func DSN(host string) string {
	return "postgres://app:" + os.Getenv("DB_PASSWORD") + "@" + host + "/app"
}

// SessionID выдаёт идентификатор сессии из криптостойкого генератора.
func SessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Emails читает адреса пользователей и проверяет ошибку итерации.
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
	return out, rows.Err()
}

// Port разбирает порт; пустая строка — порт по умолчанию.
func Port(s string) (int, error) {
	if s == "" {
		return 8080, nil
	}
	return strconv.Atoi(s)
}
