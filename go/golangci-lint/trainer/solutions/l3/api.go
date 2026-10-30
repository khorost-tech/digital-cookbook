// Package l3 — эталон уровня 3.
package l3

import (
	"context"
	"database/sql"
	"net/http"
)

// WriteBody отдаёт заранее подготовленное тело.
func WriteBody(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// L3: подавить обоснованно
	w.Write(body) //nolint:errcheck // статус уже выбран; ошибку выдачи готового тела здесь намеренно не обрабатываем
}

// First берёт первое имя и закрывает rows и при раннем выходе.
func First(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM users")
	if err != nil {
		return "", err
	}
	defer rows.Close() //nolint:errcheck // только чтение; ошибка закрытия ничего не меняет
	if rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return "", err
		}
		return n, nil
	}
	return "", rows.Err()
}
