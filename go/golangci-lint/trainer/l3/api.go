// Package l3 — уровень 3: подавить обоснованно и включить нужный линтер.
package l3

import (
	"context"
	"database/sql"
	"net/http"
)

// WriteBody отдаёт заранее подготовленное тело. Статус уже выбран и отправлен
// WriteHeader: другой статус после ошибки Write не отправить. Причина ошибки
// может быть разной — обрыв, ErrContentLength, ErrHandlerTimeout, — и здесь её
// намеренно не обрабатывают. Задача: подавить находку errcheck на строке ниже
// узко и с причиной, а не переписывать код.
func WriteBody(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// L3: подавить обоснованно
	w.Write(body)
}

// First берёт первое имя. Здесь дефект, которого текущий конфиг не видит:
// задача — включить в .golangci.yml линтер, который его ловит, и починить.
func First(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM users")
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
