// client.go — HTTP-клиент на stdlib с двумя обязательными привычками:
// собственным http.Client с таймаутом (http.DefaultClient таймаута НЕ имеет —
// зависший сервер держал бы горутину вечно) и запросом через
// http.NewRequestWithContext, чтобы вызывающий мог отменить обращение по
// context (дедлайн, отмена родительской операции).
package client

import (
	"context"
	"io"
	"net/http"
	"time"
)

// New возвращает *http.Client с общим таймаутом на всю операцию (соединение,
// отправка, чтение ответа). Это грубая верхняя граница; точечная отмена — через
// context в конкретном запросе.
func New(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// Get выполняет GET с привязкой к ctx: если контекст отменят или истечёт его
// дедлайн, запрос прервётся и вернётся ошибка контекста. Тело читается целиком
// и возвращается вызывающему.
func Get(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(resp.Body)
}
