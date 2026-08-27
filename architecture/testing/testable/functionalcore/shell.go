package functionalcore

import (
	"context"
	"fmt"
	"time"
)

// --- ОБОЛОЧКА: всё общение с миром, минимум логики ---

// Repo — граница хранилища (шов).
type Repo interface {
	Load(ctx context.Context, orderID string) (Order, error)
	SaveDiscount(ctx context.Context, orderID string, cents int64) error
}

// Notifier — граница уведомлений (шов).
type Notifier interface {
	Notify(ctx context.Context, userID, text string) error
}

// Clock — граница времени (шов).
type Clock interface {
	Now() time.Time
}

// Service — императивная оболочка: загрузить → спросить ядро → применить.
// Своей логики почти нет: она вся в Decide. Поэтому оболочке хватает пары
// тестов на проводку, а сложные ветки правил проверяются в ядре без дублёров.
type Service struct {
	Repo     Repo
	Notifier Notifier
	Clock    Clock
}

func (s Service) Process(ctx context.Context, orderID string) (Decision, error) {
	o, err := s.Repo.Load(ctx, orderID) // I/O
	if err != nil {
		return Decision{}, fmt.Errorf("загрузка заказа: %w", err)
	}

	d := Decide(o, s.Clock.Now()) // ЧИСТОЕ решение — вся логика здесь

	if d.DiscountCents > 0 { // I/O
		if err := s.Repo.SaveDiscount(ctx, orderID, d.DiscountCents); err != nil {
			return Decision{}, fmt.Errorf("сохранение скидки: %w", err)
		}
	}
	if d.Notify { // I/O
		text := fmt.Sprintf("Скидка %d коп.: %s", d.DiscountCents, d.Reason)
		if err := s.Notifier.Notify(ctx, o.UserID, text); err != nil {
			return Decision{}, fmt.Errorf("уведомление: %w", err)
		}
	}
	return d, nil
}
