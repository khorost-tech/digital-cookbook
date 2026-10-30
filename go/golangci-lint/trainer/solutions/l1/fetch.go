// Package l1 — эталон уровня 1.
package l1

import (
	"context"
	"io"
	"net/http"
	"os"
)

// Fetch скачивает страницу и сохраняет её в файл.
func Fetch(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // тело прочитано; ошибка закрытия ничего не меняет
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Retries — сколько повторов осталось после первой попытки.
func Retries(limit int) int {
	return limit - 1
}
