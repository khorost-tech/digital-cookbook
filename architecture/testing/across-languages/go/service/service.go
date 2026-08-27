// Package service — тонкий слой над доменом: посчитать цену, сохранить заказ,
// уведомить. Нужен, чтобы показать ПРИЁМ 3 — дублёров (мок/стаб/фейк) — на
// одинаковой задаче в трёх языках.
package service

import (
	"context"
	"fmt"

	"tech.khorost/across-languages-go/pricing"
)

// Order — сохранённый заказ.
type Order struct {
	ID        string
	UserID    string
	TotalCent int64 // сумма до скидки
	PriceCent int64 // к оплате
}

// Store — граница нашего кода: хранилище заказов. Порт, а не деталь: реализаций
// две — Postgres в проде и фейк в тестах.
type Store interface {
	Save(ctx context.Context, o Order) error
	ByUser(ctx context.Context, userID string) ([]Order, error)
}

// Notifier — вторая граница: отправка уведомления.
type Notifier interface {
	Notify(ctx context.Context, userID, text string) error
}

// OrderService — то, что тестируем.
type OrderService struct {
	Store    Store
	Notifier Notifier
}

// Create считает цену, сохраняет заказ и уведомляет пользователя.
func (s OrderService) Create(ctx context.Context, id, userID string, totalCents int64) (Order, error) {
	if totalCents < 0 {
		return Order{}, fmt.Errorf("сумма заказа отрицательна: %d", totalCents)
	}
	price := pricing.Price(totalCents)
	if discountBug {
		price = totalCents // БАГ (только под тегом bug): скидка потеряна
	}
	o := Order{
		ID:        id,
		UserID:    userID,
		TotalCent: totalCents,
		PriceCent: price,
	}
	if err := s.Store.Save(ctx, o); err != nil {
		return Order{}, fmt.Errorf("сохранение заказа: %w", err)
	}
	// Уведомление — не критично: заказ уже сохранён, ошибку не эскалируем.
	_ = s.Notifier.Notify(ctx, userID, fmt.Sprintf("Заказ %s принят, к оплате %d коп.", id, o.PriceCent))
	return o, nil
}
