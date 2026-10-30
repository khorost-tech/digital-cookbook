// Package l1 — уровень 1: замечания, которые чинятся одной строкой.
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
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	os.WriteFile(path, data, 0o600)
	return nil
}

// Retries — сколько повторов осталось после первой попытки.
func Retries(limit int) int {
	left := limit
	left = limit - 1
	return left
}
