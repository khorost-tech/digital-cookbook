// Package httpx — HTTP-клиент без закрытия тела и без контекста.
package httpx

import (
	"context"
	"io"
	"net/http"
)

// Status читает только код ответа; тело не закрыто — соединение не вернётся в пул.
func Status(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req) // want bodyclose
	if err != nil {
		return 0, err
	}
	return resp.StatusCode, nil
}

// Fetch без контекста: вызов нельзя отменить и ограничить сроком.
func Fetch(url string) ([]byte, error) {
	resp, err := http.Get(url) // want noctx // noise gosec
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() // noise errcheck
	return io.ReadAll(resp.Body)
}
